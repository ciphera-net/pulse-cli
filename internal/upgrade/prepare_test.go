package upgrade

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// releaseServer publishes one archive and its signature over httptest, signed
// with a key this test controls, and swaps that key in for the embedded one.
//
// Swapping the key is what makes an end-to-end test possible at all: the real
// private key lives in Vault and reaches nothing but CI, so a test that signs
// with it could not exist. Everything else on the path — asset selection,
// download, verification, extraction — is the production code.
func releaseServer(t *testing.T, key *ecdsa.PrivateKey, corrupt bool) (*Client, *Release) {
	t.Helper()

	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	original := cosignPub
	cosignPub = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	t.Cleanup(func() { cosignPub = original })

	name := "pulse_1.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	if runtime.GOOS == "windows" {
		name = "pulse_1.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip"
	}

	archive := tarGz(t, map[string]string{BinaryName(runtime.GOOS): "the new pulse binary"})
	if runtime.GOOS == "windows" {
		archive = zipped(t, map[string]string{BinaryName(runtime.GOOS): "the new pulse binary"})
	}
	// * The signature covers the real archive; the SERVED archive is a different,
	// * perfectly valid one containing a different binary. That is what a
	// * compromised mirror actually looks like — not a corrupted download.
	// *
	// * Substituting a valid archive rather than flipping a byte matters for what
	// * this proves: a torn gzip stream is caught by the tar reader, so a Prepare
	// * with no verification at all would still fail and the test would pass for
	// * the wrong reason. A well-formed substitute can only be caught by the
	// * signature.
	sig := signBlob(t, key, archive)
	if corrupt {
		if runtime.GOOS == "windows" {
			archive = zipped(t, map[string]string{BinaryName(runtime.GOOS): "a substituted binary"})
		} else {
			archive = tarGz(t, map[string]string{BinaryName(runtime.GOOS): "a substituted binary"})
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/"+name, func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
	mux.HandleFunc("/"+name+".sig", func(w http.ResponseWriter, _ *http.Request) { w.Write(sig) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rel := &Release{
		Tag: "v1.1.0",
		URL: srv.URL + "/releases/tag/v1.1.0",
		Assets: []Asset{
			{Name: name, URL: srv.URL + "/" + name, Size: int64(len(archive))},
			{Name: name + ".sig", URL: srv.URL + "/" + name + ".sig", Size: int64(len(sig))},
		},
	}
	return NewClient("pulse-cli/test"), rel
}

// * The whole download path, end to end: pick the asset for this platform, fetch
// * it, verify it against the release key, open it.
func TestPrepareReturnsTheVerifiedBinary(t *testing.T) {
	key := testKey(t)
	c, rel := releaseServer(t, key, false)

	var progress []string
	bin, err := Prepare(context.Background(), c, rel, func(m string) { progress = append(progress, m) })
	if err != nil {
		t.Fatalf("Prepare rejected a correctly signed release: %v", err)
	}
	if string(bin) != "the new pulse binary" {
		t.Errorf("extracted %q", bin)
	}
	if len(progress) < 2 {
		t.Errorf("progress = %v, want the download and the verification named", progress)
	}
}

// * The half that matters. A modified archive must not produce bytes, and the
// * message has to say that nothing was written — a user who sees a failed
// * upgrade needs to know whether they still have a working CLI.
func TestPrepareRefusesAnArchiveTheKeyDidNotSign(t *testing.T) {
	key := testKey(t)
	c, rel := releaseServer(t, key, true)

	bin, err := Prepare(context.Background(), c, rel, nil)
	if err == nil {
		t.Fatal("a modified archive was prepared for installation")
	}
	if bin != nil {
		t.Error("Prepare returned bytes alongside an error; a careless caller would install them")
	}
	if !strings.Contains(err.Error(), "Nothing has been changed on disk") {
		t.Errorf("the refusal does not say the machine is untouched: %v", err)
	}
}
