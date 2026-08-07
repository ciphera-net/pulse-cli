package upgrade

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// cosignPub is the release-signing public key, compiled into the binary.
//
// Embedded rather than fetched: a key downloaded at upgrade time is checked
// against nothing, so whoever can serve you a modified archive can serve you the
// key that signs it. The key that verifies an upgrade has to be the one that
// shipped with the binary already on disk.
//
// It is a byte-for-byte copy of cosign.pub at the repository root — the file the
// README tells people to verify against by hand. TestEmbeddedKeyMatchesRepoRoot
// fails if the two ever drift.
//
//go:embed cosign.pub
var cosignPub []byte

// ErrBadSignature is a blob the release key did not sign. Nothing that produces
// it is ever written over the running binary.
var ErrBadSignature = errors.New("signature does not match the Pulse release key")

// PublicKey parses the embedded release key.
func PublicKey() (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(cosignPub)
	if block == nil {
		return nil, errors.New("embedded release key is not PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("embedded release key: %w", err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		// * The type assertion is the check. An RSA or Ed25519 key would parse
		// * happily and then be passed to an ECDSA verifier that cannot use it;
		// * asserting here means a key swap is a build-time-visible failure
		// * rather than a verification that quietly always fails.
		return nil, fmt.Errorf("embedded release key is %T, want an ECDSA key", parsed)
	}
	return pub, nil
}

// Verify checks a cosign detached signature over a blob.
//
// This is deliberately not a call to the cosign binary, and not the sigstore Go
// module: a `sign-blob` signature is an ASN.1 ECDSA signature over the blob's
// SHA-256, base64-encoded — five standard-library packages, no dependency, and
// no assumption that the machine being upgraded has cosign installed. The
// published .sig files are 96 bytes because that is base64 of a ~70-byte DER
// signature.
//
// Confirmed against the real v1.0.0 artefacts: the published archive verifies,
// and the same archive with one byte changed does not.
func Verify(pub *ecdsa.PublicKey, blob, signature []byte) error {
	if pub == nil {
		return errors.New("no release key")
	}
	// * The .sig published by cosign is base64 TEXT, not raw DER. Feeding the
	// * file straight to VerifyASN1 fails on every input, including a good one —
	// * a verifier that rejects everything looks exactly like a verifier that
	// * works, which is why the accept case is tested alongside the reject case.
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil {
		return fmt.Errorf("%w: signature is not base64 (%v)", ErrBadSignature, err)
	}

	digest := sha256.Sum256(blob)
	if !ecdsa.VerifyASN1(pub, digest[:], der) {
		return ErrBadSignature
	}
	return nil
}
