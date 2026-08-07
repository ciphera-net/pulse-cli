package cli

import "testing"

// * A withheld metric must be an EMPTY CSV cell.
// *
// * Not 0 — that is a number the server explicitly refused to state, and a
// * spreadsheet will happily sum it into a total that is simply wrong. Not the
// * em dash the table uses either: a column of "—" imports as text, so the
// * spreadsheet stops treating the column as numeric and every formula over it
// * breaks. Empty is the one value every tool already reads as "no data".
func TestWithheldMetricIsAnEmptyCSVCell(t *testing.T) {
	if got := csvInt(nil); got != "" {
		t.Errorf("csvInt(nil) = %q, want an empty cell", got)
	}
	if got := csvFloat(nil); got != "" {
		t.Errorf("csvFloat(nil) = %q, want an empty cell", got)
	}
}

// * The complement: a real value must be a raw, unformatted number a spreadsheet
// * can compute on. "62.4%" and "1m 47s" are for the table; a CSV consumer needs
// * 62.4 and 107.
func TestCSVValuesAreRawNumbers(t *testing.T) {
	i := 1284
	if got := csvInt(&i); got != "1284" {
		t.Errorf("csvInt = %q, want \"1284\" — no thousands separator in CSV", got)
	}
	f := 62.4
	if got := csvFloat(&f); got != "62.4" {
		t.Errorf("csvFloat = %q, want \"62.4\" — no percent sign, no rounding", got)
	}
	zero := 0
	if got := csvInt(&zero); got != "0" {
		t.Errorf("a real zero must still be 0 in CSV, got %q", got)
	}
	long := 107.33333333333333
	if got := csvFloat(&long); got != "107.33333333333333" {
		t.Errorf("csvFloat truncated precision: %q", got)
	}
}
