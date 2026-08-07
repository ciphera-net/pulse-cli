package render

import (
	"encoding/csv"
	"fmt"
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
func (p *Printer) Table(t Table) {
	if p.Mode == ModeCSV {
		p.tableCSV(t)
		return
	}
	p.tableText(t)
}

func (p *Printer) tableCSV(t Table) {
	// * csv.Writer keeps its own sticky error, and every one of them arrived
	// * from the stream underneath — which kept it too, and is what Execute
	// * checks. Dropping them here loses nothing.
	w := csv.NewWriter(p.out)
	_ = w.Write(t.Headers)
	for _, row := range t.Rows {
		_ = w.Write(row)
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
func (p *Printer) CSVRecords(headers []string, rows [][]string) error {
	w := csv.NewWriter(p.out)
	if err := w.Write(headers); err != nil {
		return fmt.Errorf("writing csv header: %w", err)
	}
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			return fmt.Errorf("writing csv row: %w", err)
		}
	}
	w.Flush()
	return w.Error()
}
