package render

import (
	"bytes"
	"strings"
	"testing"
)

// * The security property lives at the SHARED choke point now, not in any one
// * command: a dangerous cell must never reach the terminal or a CSV file raw,
// * regardless of which command built the row. Table is the renderer both
// * `breakdown` and `realtime` (and anything written after this) call.
func TestTableSanitizesControlCharactersInCellsInBothModes(t *testing.T) {
	evil := "/pricing" + string(rune(0x1b)) + "]0;pwned" + string(rune(0x07)) + string(rune(0x202e)) + "reversed"

	for _, mode := range []Mode{ModeTable, ModeCSV} {
		t.Run(string(mode), func(t *testing.T) {
			out := &bytes.Buffer{}
			p := NewPrinterTo(out, &bytes.Buffer{}, mode)
			p.Table(Table{
				Headers: []string{"PAGE", "VISITORS"},
				Right:   []bool{false, true},
				Rows:    [][]string{{evil, "12"}},
			})

			got := out.String()
			for _, r := range []rune{0x1b, 0x07, 0x202e} {
				if strings.ContainsRune(got, r) {
					t.Errorf("raw U+%04X reached output in %s mode: %q", r, mode, got)
				}
			}
			if !strings.Contains(got, `\u001B`) {
				t.Errorf("escaped ESC did not appear in %s mode: %q", mode, got)
			}
			if !strings.Contains(got, "/pricing") {
				t.Errorf("harmless part of the cell was lost in %s mode: %q", mode, got)
			}
		})
	}
}

// * CSVRecords is the SECOND CSV choke point (breakdown and realtime both
// * build their own CSV rows rather than going through Table) and must give
// * the same guarantee independently — a command that never calls Table must
// * not be able to skip sanitisation by using this method instead.
func TestCSVRecordsSanitizesControlCharacters(t *testing.T) {
	evil := "/blog" + string(rune(0x00)) + string(rune(0x202e))
	out := &bytes.Buffer{}
	p := NewPrinterTo(out, &bytes.Buffer{}, ModeCSV)

	if err := p.CSVRecords([]string{"path", "visitors"}, [][]string{{evil, "3"}}); err != nil {
		t.Fatalf("CSVRecords: %v", err)
	}

	got := out.String()
	for _, r := range []rune{0x00, 0x202e} {
		if strings.ContainsRune(got, r) {
			t.Errorf("raw U+%04X reached the CSV output: %q", r, got)
		}
	}
	if !strings.Contains(got, `\u0000`) {
		t.Errorf("escaped NUL did not appear: %q", got)
	}
}

// * Headers are literal strings this codebase wrote, never a value the API
// * returned — sanitising them would be harmless but pointless, and this pins
// * that only ROW cells are touched.
func TestTableDoesNotAlterHeaders(t *testing.T) {
	out := &bytes.Buffer{}
	p := NewPrinterTo(out, &bytes.Buffer{}, ModeTable)
	p.Table(Table{Headers: []string{"PAGE", "VISITORS"}, Rows: [][]string{{"/", "1"}}})
	if !strings.Contains(out.String(), "PAGE") || !strings.Contains(out.String(), "VISITORS") {
		t.Errorf("headers did not survive unchanged: %q", out.String())
	}
}

// * CWE-1236: a cell a spreadsheet would read as a formula must be forced back
// * to text with a leading apostrophe, but a plain negative or positive number
// * must not be — otherwise a numeric column stops being one a spreadsheet can
// * sum. Table-driven against the exact cases the review named.
func TestCSVFormulaGuard(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"equals-prefixed formula", "=cmd|'/C calc'!A1", "'=cmd|'/C calc'!A1"},
		{"at-prefixed", "@handle", "'@handle"},
		{"leading plus that is a real number", "+1", "+1"},
		{"leading minus that is a real number", "-5", "-5"},
		{"ordinary path, no trigger character", "/pricing", "/pricing"},
		{"empty string", "", ""},
		{"leading plus, NOT a number", "+not-a-number", "'+not-a-number"},
		{"leading minus, NOT a number", "-not-a-number", "'-not-a-number"},
		{"decimal number", "-5.5", "-5.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := csvFormulaGuard(tc.in); got != tc.want {
				t.Errorf("csvFormulaGuard(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// * The formula guard is a CSV-only defence — a table cell opens in a
// * terminal, never a spreadsheet, and prepending an apostrophe there would be
// * a visible corruption of the value with no compensating benefit.
func TestCSVFormulaGuardDoesNotApplyToTableMode(t *testing.T) {
	out := &bytes.Buffer{}
	p := NewPrinterTo(out, &bytes.Buffer{}, ModeTable)
	p.Table(Table{Headers: []string{"VALUE"}, Rows: [][]string{{"=cmd|'/C calc'!A1"}}})

	if strings.Contains(out.String(), "'=cmd") {
		t.Errorf("table mode applied the CSV formula guard: %q", out.String())
	}
	if !strings.Contains(out.String(), "=cmd") {
		t.Errorf("table output lost the value entirely: %q", out.String())
	}
}

// * The complement: the same dangerous cell IN CSV mode must both be escaped
// * (control characters) and formula-guarded (CWE-1236) — proving the two
// * defences compose rather than one silently replacing the other.
func TestTableCSVModeAppliesBothControlSanitisationAndFormulaGuard(t *testing.T) {
	out := &bytes.Buffer{}
	p := NewPrinterTo(out, &bytes.Buffer{}, ModeCSV)
	p.Table(Table{Headers: []string{"VALUE"}, Rows: [][]string{{"=cmd" + string(rune(0x1b)) + "pwned"}}})

	got := out.String()
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("raw ESC reached CSV output: %q", got)
	}
	if !strings.Contains(got, `'=cmd`) {
		t.Errorf("formula guard did not fire on a control-sanitised cell starting with '=': %q", got)
	}
}
