package cli_test

import (
	"bufio"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestStdioStdoutCarriesOnlyProtocolFrames is the framing assertion §16 owed.
//
// In stdio mode stdout IS the JSON-RPC channel. A single stray byte on it — a
// fmt.Println left in a handler, a library that logs to stdout, a debug print
// that survived review — corrupts the stream. The failure does not look like a
// stray line to the host: it looks like a protocol error, and the server reads
// as broken rather than as chatty.
//
// Nothing else in the suite catches this. The SDK-driven tests use a client
// that parses what it expects and ignores the rest, so a stray line can sail
// past them. This test reads the raw pipe and insists that EVERY line is a
// JSON-RPC message.
func TestStdioStdoutCarriesOnlyProtocolFrames(t *testing.T) {
	bin := buildPulse(t)

	cmd := exec.Command(bin, "mcp")
	// A key that will never authenticate, pointed at a port nothing listens on.
	// The tools must still FAIL as tool errors on the protocol channel — an
	// error path is exactly where a stray print is most likely to hide.
	cmd.Env = append(cmd.Environ(),
		"PULSE_API_KEY=pulse_sk_live_framing", "PULSE_API_URL=http://127.0.0.1:9")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	// stdin is held OPEN across all three requests. Closing it immediately after
	// writing ends the session before responses flush — which is a property of
	// the harness, not of the server, and cost an hour to work out once.
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"framing","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"pulse_list_sites","arguments":{}}}`,
	}
	for _, f := range frames {
		if _, err := stdin.Write([]byte(f + "\n")); err != nil {
			t.Fatalf("write frame: %v", err)
		}
	}

	type line struct {
		raw string
		ok  bool
	}
	got := make(chan line, 16)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
		for sc.Scan() {
			text := sc.Text()
			if strings.TrimSpace(text) == "" {
				continue
			}
			var m map[string]any
			err := json.Unmarshal([]byte(text), &m)
			got <- line{raw: text, ok: err == nil && m["jsonrpc"] == "2.0"}
		}
		close(got)
	}()

	// Three responses are expected (initialize, tools/list, tools/call); the
	// notification gets none.
	seen, ids := 0, map[float64]bool{}
	deadline := time.After(90 * time.Second)
	for seen < 3 {
		select {
		case l, open := <-got:
			if !open {
				t.Fatalf("stdout closed after %d framed responses", seen)
			}
			if !l.ok {
				// The whole point of the test.
				t.Fatalf("stdout carried a line that is not a JSON-RPC frame — "+
					"this corrupts the session for every host:\n  %.200q", l.raw)
			}
			var m map[string]any
			_ = json.Unmarshal([]byte(l.raw), &m)
			if id, isNum := m["id"].(float64); isNum {
				ids[id] = true
			}
			seen++
		case <-deadline:
			t.Fatalf("timed out after %d framed responses", seen)
		}
	}

	for _, want := range []float64{1, 2, 3} {
		if !ids[want] {
			t.Errorf("no response for request id %v", want)
		}
	}
}
