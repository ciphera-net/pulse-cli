package render

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Thousands groups an integer for reading: 1284 becomes 1,284.
func Thousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}

	var out []byte
	for i, d := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, d)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// HumanDuration renders seconds the way a person reads a session length.
func HumanDuration(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	total := int(seconds + 0.5)
	switch {
	case total < 60:
		return fmt.Sprintf("%ds", total)
	case total < 3600:
		return fmt.Sprintf("%dm %02ds", total/60, total%60)
	default:
		return fmt.Sprintf("%dh %02dm", total/3600, (total%3600)/60)
	}
}

// Relative renders a timestamp as an age.
//
// nil is "never", which is a genuinely different state from "a long time ago"
// and is what makes "this key has never been used, safe to delete" answerable.
// Rendering a nil as the zero time is how a UI ends up claiming a key was last
// used in the year 1.
func Relative(t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := time.Since(*t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hr ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	default:
		return t.Local().Format("2 Jan 2006")
	}
}

// Expiry renders an expiry date with the distance to it, because the date alone
// does not answer the question anyone actually has.
func Expiry(t time.Time) string {
	days := int(time.Until(t).Hours() / 24)
	switch {
	case days < 0:
		return fmt.Sprintf("%s (EXPIRED %s ago)", t.Local().Format("2006-01-02"), plural(-days, "1 day", fmt.Sprintf("%d days", -days)))
	case days == 0:
		return fmt.Sprintf("%s (expires today)", t.Local().Format("2006-01-02"))
	case days == 1:
		return fmt.Sprintf("%s (1 day)", t.Local().Format("2006-01-02"))
	default:
		return fmt.Sprintf("%s (%d days)", t.Local().Format("2006-01-02"), days)
	}
}
