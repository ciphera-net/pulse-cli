package upgrade

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"
)

// tarGz builds an archive shaped like a real release: the binary alongside the
// README and LICENSE that .goreleaser.yaml packs with it.
func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipped(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBinaryReadsBothPublishedFormats(t *testing.T) {
	files := map[string]string{
		"pulse":     "the mach-o binary",
		"README.md": "# pulse",
		"LICENSE":   "Apache-2.0",
	}
	got, err := ExtractBinary("pulse_1.1.0_darwin_arm64.tar.gz", tarGz(t, files), "pulse")
	if err != nil {
		t.Fatalf("tar.gz: %v", err)
	}
	if string(got) != "the mach-o binary" {
		t.Errorf("tar.gz: extracted %q", got)
	}

	win := map[string]string{"pulse.exe": "the PE binary", "README.md": "# pulse"}
	got, err = ExtractBinary("pulse_1.1.0_windows_amd64.zip", zipped(t, win), "pulse.exe")
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	if string(got) != "the PE binary" {
		t.Errorf("zip: extracted %q", got)
	}
}

// * The archive is the last place a name from the network could turn into a
// * path. Extraction reads bytes and never opens a file, so an entry called
// * ../../.ssh/authorized_keys is simply not the binary and is skipped — but the
// * test says so out loud, because the day someone "improves" this into an
// * extract-to-disk loop is the day it matters.
func TestExtractBinaryIgnoresEverythingButTheBinary(t *testing.T) {
	files := map[string]string{
		"../../.ssh/authorized_keys": "ssh-ed25519 AAAA…",
		"pulse":                      "the real binary",
	}
	got, err := ExtractBinary("x.tar.gz", tarGz(t, files), "pulse")
	if err != nil {
		t.Fatalf("ExtractBinary: %v", err)
	}
	if string(got) != "the real binary" {
		t.Errorf("extracted %q", got)
	}
}

func TestExtractBinaryReportsWhatItCannotRead(t *testing.T) {
	if _, err := ExtractBinary("pulse.tar.gz", tarGz(t, map[string]string{"README.md": "x"}), "pulse"); err == nil {
		t.Error("an archive with no pulse binary was accepted")
	}
	if _, err := ExtractBinary("pulse.tar.gz", []byte("not gzip at all"), "pulse"); err == nil {
		t.Error("a non-gzip body was accepted as a tar.gz")
	}
	if _, err := ExtractBinary("pulse.rpm", []byte("x"), "pulse"); err == nil {
		t.Error("an unknown archive format was accepted")
	}
	if _, err := ExtractBinary("pulse.tar.gz", tarGz(t, map[string]string{"pulse": ""}), "pulse"); err == nil {
		t.Error("an empty binary was accepted; the upgrade would install a zero-byte pulse")
	}
}

func TestBinaryNameFollowsThePlatform(t *testing.T) {
	if got := BinaryName("windows"); got != "pulse.exe" {
		t.Errorf("BinaryName(windows) = %q", got)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if got := BinaryName(goos); got != "pulse" {
			t.Errorf("BinaryName(%s) = %q", goos, got)
		}
	}
}
