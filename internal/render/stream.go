package render

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
)

// stream is an output stream that remembers the first write that failed.
//
// It exists because a CLI that exits 0 after a failed write is worse than one
// that crashes: `pulse export daily --from … > week.csv` under a file-size limit
// wrote 1,024 bytes, discarded the EFBIG, and exited 0 — so a script saw success
// and kept a CSV that stops mid-row. Every byte a command prints goes through
// one of these, which is why no call site has to remember to check anything.
//
// After a failure the stream stops writing. There is nothing to gain from
// pushing the rest of an export at a disk that has already refused it, and under
// `ulimit -f` each further write raises SIGXFSZ again.
type stream struct {
	// name is how the failure is described to the user — "stdout" or "stderr".
	// Which stream broke is most of the diagnosis.
	name string
	w    io.Writer
	err  error
}

func (s *stream) Write(b []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n, err := s.w.Write(b)
	if err == nil && n < len(b) {
		// * A short write with no error still loses bytes. io.Writer forbids
		// * one, but a wrapper further down can get it wrong, and silently
		// * truncated output is the exact thing this type exists to prevent.
		err = io.ErrShortWrite
	}
	s.err = err
	return n, err
}

// failure describes the first failed write, or returns nil.
//
// A broken pipe is not a failure — see isBrokenPipe.
func (s *stream) failure() error {
	if s.err == nil || isBrokenPipe(s.err) {
		return nil
	}
	return fmt.Errorf("writing to %s: %w", s.name, s.err)
}

// isBrokenPipe reports whether a write failed because the reader went away.
//
// `pulse sites ls | head -3` closes the pipe the moment head has its three
// lines, and that is the pipeline working, not failing. Turning it into a
// non-zero exit would break piping into head, less and `grep -q` for every user
// of a published CLI — so a closed pipe stays exit 0, which is also what the
// Go runtime already does by raising SIGPIPE for writes to fd 1 and 2.
//
// errors.Is against the errno is the mechanism. The string check is only for
// Windows, which fails a write to a closed pipe with ERROR_BROKEN_PIPE or
// ERROR_NO_DATA — syscall.Errno values that are not EPIPE and cannot be named
// from a file that also builds for Unix, so their rendered message is all that
// is left to match on.
func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EPIPE) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, windows := range []string{
		"the pipe has been ended",  // ERROR_BROKEN_PIPE
		"the pipe is being closed", // ERROR_NO_DATA
		"broken pipe",              // EPIPE rendered, for an errno we never see as one
	} {
		if strings.Contains(msg, windows) {
			return true
		}
	}
	return false
}
