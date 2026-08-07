package render

import (
	"bytes"
	"strings"
	"testing"
)

func testPrinter(mode Mode) (*Printer, *bytes.Buffer, *bytes.Buffer) {
	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	return &Printer{Out: out, Err: errBuf, Mode: mode, Color: false}, out, errBuf
}

// * Everything that is not the answer goes to stderr.
// *
// * This is what makes `pulse stats --json | jq` and `pulse export daily > week.csv`
// * work at all. A note on stdout becomes a corrupt CSV row or a jq parse error,
// * and the failure surfaces in the user's pipeline rather than here — which is
// * the worst place to debug it.
func TestOnlyDataGoesToStdout(t *testing.T) {
	p, out, errBuf := testPrinter(ModeTable)

	p.Note("resolving 7d in the site's timezone")
	p.Warn("quota is nearly exhausted")
	p.Success("default site set")

	if out.Len() != 0 {
		t.Errorf("notes, warnings and confirmations must not reach stdout; got %q", out.String())
	}
	for _, want := range []string{"resolving", "quota", "default site"} {
		if !strings.Contains(errBuf.String(), want) {
			t.Errorf("stderr is missing %q: %q", want, errBuf.String())
		}
	}
}

// * The complement: the payload must reach stdout, byte-for-byte and with
// * nothing prepended.
func TestJSONGoesToStdoutUnmodified(t *testing.T) {
	p, out, errBuf := testPrinter(ModeJSON)

	body := `{"meta":{"suppressed":true},"data":{"visitors":null,"unknown_future_field":1}}`
	p.JSON([]byte(body))

	got := strings.TrimRight(out.String(), "\n")
	if got != body {
		t.Errorf("JSON output was altered.\n got: %s\nwant: %s", got, body)
	}
	if errBuf.Len() != 0 {
		t.Errorf("JSON output must not write to stderr; got %q", errBuf.String())
	}
}

// * Colour is opt-out by capability, not by flag. When Color is false — which is
// * what NewPrinter decides for a pipe — not one escape byte may appear, or the
// * bytes end up in a CSV file or a log.
func TestNoAnsiEscapesWhenColourIsOff(t *testing.T) {
	p, out, errBuf := testPrinter(ModeTable)

	p.Printf("%s\n", p.Bold("bold"))
	p.Printf("%s\n", p.Dim("dim"))
	p.Note("a note")
	p.Warn("a warning")
	p.Success("done")

	combined := out.String() + errBuf.String()
	if strings.ContainsRune(combined, 0x1b) {
		t.Errorf("an ANSI escape survived with Color=false: %q", combined)
	}
}

// * A withheld metric has to survive the table renderer too. Rendering it
// * correctly and then formatting it into a numeric column as 0 would defeat the
// * entire suppression path at the last step.
func TestWithheldValuesSurviveTheTable(t *testing.T) {
	p, out, _ := testPrinter(ModeTable)

	p.Table(Table{
		Headers: []string{"METRIC", "VALUE"},
		Right:   []bool{false, true},
		Rows:    [][]string{{"Visitors", Withheld}, {"Pageviews", Withheld}},
	})

	body := out.String()
	if !strings.Contains(body, Withheld) {
		t.Errorf("the withheld marker did not reach the table: %q", body)
	}
	if strings.ContainsAny(strings.ReplaceAll(body, "METRIC", ""), "0123456789") {
		t.Errorf("a digit appeared in a table of withheld values: %q", body)
	}
}

// * Thousands separators are the difference between reading 1284 and 12840 at a
// * glance, which is most of what a stats table is for.
func TestThousandsGrouping(t *testing.T) {
	for in, want := range map[int]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000",
		1284: "1,284", 1000000: "1,000,000", -4210: "-4,210",
	} {
		if got := Thousands(in); got != want {
			t.Errorf("Thousands(%d) = %q, want %q", in, got, want)
		}
	}
}

// * "never" is a different fact from "a long time ago". A nil rendered as a zero
// * date is how a UI ends up claiming a key was last used in the year 1, and
// * "never used, safe to delete" stops being answerable.
func TestNilTimestampRendersAsNever(t *testing.T) {
	if got := Relative(nil); got != "never" {
		t.Errorf("Relative(nil) = %q, want \"never\"", got)
	}
	if strings.Contains(Relative(nil), "0001") {
		t.Error("a nil timestamp leaked the zero date")
	}
}
