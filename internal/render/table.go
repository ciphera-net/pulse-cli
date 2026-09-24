package render

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Table is a simple aligned table.
//
// Alignment is per column, because a numeric column that is left-aligned makes
// two figures of different magnitude impossible to compare at a glance — which
// is most of what a stats table is for.
type Table struct {
	Headers []string
	Rows    [][]string
	Right   []bool // true = right-align this column
}

// Render writes the table in whatever mode the printer is in.
//
// Every ROW CELL is passed through SanitizeControl before it reaches either
// renderer below — this is the shared choke point, so a command that renders
// a visitor-supplied value (a page path, a referrer, a realtime top path)
// cannot forget to sanitise it, and a future command gets the protection for
// free. Headers are not sanitised: they are literal strings this codebase
// wrote, never a value the API returned.
func (p *Printer) Table(t Table) {
	t.Rows = sanitizeCells(t.Rows)
	if p.Mode == ModeCSV {
		p.tableCSV(t)
		return
	}
	p.tableText(t)
}

// sanitizeCells returns rows with every cell passed through SanitizeControl.
func sanitizeCells(rows [][]string) [][]string {
	if len(rows) == 0 {
		return rows
	}
	out := make([][]string, len(rows))
	for i, row := range rows {
		sanitized := make([]string, len(row))
		for j, cell := range row {
			sanitized[j] = SanitizeControl(cell)
		}
		out[i] = sanitized
	}
	return out
}

func (p *Printer) tableCSV(t Table) {
	// * csv.Writer keeps its own sticky error, and every one of them arrived
	// * from the stream underneath — which kept it too, and is what Execute
	// * checks. Dropping them here loses nothing.
	w := csv.NewWriter(p.out)
	_ = w.Write(t.Headers)
	for _, row := range t.Rows {
		// * t.Rows is already control-sanitised (Table, above) — csvFormulaGuard
		// * only needs to add the CSV-specific defence.
		_ = w.Write(guardFormulaRow(row))
	}
	w.Flush()
}

func (p *Printer) tableText(t Table) {
	widths := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		widths[i] = displayWidth(h)
	}
	for _, row := range t.Rows {
		for i, cell := range row {
			if i < len(widths) && displayWidth(cell) > widths[i] {
				widths[i] = displayWidth(cell)
			}
		}
	}

	p.Printf("  %s\n", p.Dim(joinCells(t.Headers, widths, t.Right)))
	for _, row := range t.Rows {
		p.Printf("  %s\n", joinCells(row, widths, t.Right))
	}
}

func joinCells(cells []string, widths []int, right []bool) string {
	var sb strings.Builder
	for i, cell := range cells {
		if i > 0 {
			sb.WriteString("  ")
		}
		pad := widths[i] - displayWidth(cell)
		if pad < 0 {
			pad = 0
		}
		if i < len(right) && right[i] {
			sb.WriteString(strings.Repeat(" ", pad))
			sb.WriteString(cell)
		} else {
			sb.WriteString(cell)
			// * No trailing pad on the last column: it produces invisible
			// * whitespace that shows up when someone copies the output.
			if i < len(cells)-1 {
				sb.WriteString(strings.Repeat(" ", pad))
			}
		}
	}
	return sb.String()
}

// displayWidth counts runes rather than bytes, so a domain with an accent or a
// withheld em dash does not throw the column off.
func displayWidth(s string) int {
	return utf8.RuneCountInString(s)
}

// KeyValue prints an aligned label/value block — the shape used for a single
// record, where a table of one row reads worse than a list.
func (p *Printer) KeyValue(pairs [][2]string) {
	width := 0
	for _, kv := range pairs {
		if displayWidth(kv[0]) > width {
			width = displayWidth(kv[0])
		}
	}
	for _, kv := range pairs {
		pad := strings.Repeat(" ", width-displayWidth(kv[0]))
		p.Printf("  %s%s  %s\n", p.Dim(kv[0]), pad, kv[1])
	}
}

// CSVRecords writes arbitrary records to stdout as CSV.
//
// The other CSV choke point (Table's CSV mode is the first) — every cell here
// is also control-sanitised and formula-guarded, so a command that builds its
// own rows instead of going through Table (breakdown and realtime both do,
// for their non-tabular CSV shape) gets the same protection.
func (p *Printer) CSVRecords(headers []string, rows [][]string) error {
	w := csv.NewWriter(p.out)
	if err := w.Write(headers); err != nil {
		return fmt.Errorf("writing csv header: %w", err)
	}
	for _, row := range rows {
		sanitized := make([]string, len(row))
		for i, cell := range row {
			sanitized[i] = SanitizeControl(cell)
		}
		if err := w.Write(guardFormulaRow(sanitized)); err != nil {
			return fmt.Errorf("writing csv row: %w", err)
		}
	}
	w.Flush()
	return w.Error()
}

// guardFormulaRow applies csvFormulaGuard to every cell of an already
// control-sanitised row.
func guardFormulaRow(row []string) []string {
	out := make([]string, len(row))
	for i, cell := range row {
		out[i] = csvFormulaGuard(cell)
	}
	return out
}

// csvFormulaGuard defends against CSV formula injection (CWE-1236): a cell
// opened by a spreadsheet application whose first character is one that
// starts a formula (=, +, -, @) or that some spreadsheet builds treat
// specially at the start of a field (a literal tab or CR) is written with a
// single leading apostrophe, which every major spreadsheet renders as "force
// this to text" and strips from what is displayed.
//
// A visitor's browser can put a value like a UTM parameter into a row this
// CLI exports — breakdown has no privacy floor, and even the fields that do
// carry one still surface the FILTER value the operator typed, which could
// itself be pasted from somewhere untrusted. Opening `=cmd|'/C calc'!A1` in
// Excel runs it; this is the standard mitigation.
//
// The exception is a cell that parses whole as a number: "+1" and "-5" are
// ordinary signed integers in a numeric column, and quoting them would turn a
// column of numbers a spreadsheet can sum into a column of text it cannot.
// Applied to cells that have ALREADY passed through SanitizeControl, so a
// leading tab or CR here would only ever be one this renderer's own caller
// wrote directly into a row (bypassing Table/CSVRecords' sanitisation) —
// kept as defence in depth rather than removed as unreachable.
func csvFormulaGuard(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
	default:
		return s
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return s
	}
	return "'" + s
}
