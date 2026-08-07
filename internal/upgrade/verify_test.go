package upgrade

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"testing"
)

// signBlob produces exactly what `cosign sign-blob --output-signature` writes: a
// base64-encoded ASN.1 ECDSA signature over the blob's SHA-256.
func signBlob(t *testing.T, key *ecdsa.PrivateKey, blob []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(blob)
	der, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("signing the test blob: %v", err)
	}
	return []byte(base64.StdEncoding.EncodeToString(der) + "\n")
}

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	// * P-256, because that is what cosign's default key is and what the real
	// * cosign.pub in this repository holds.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a test key: %v", err)
	}
	return key
}

// * Both halves in one test, on purpose.
// *
// * A rejection test on its own proves nothing: a verifier with the base64 decode
// * removed, or one that hashes the wrong bytes, rejects a tampered archive AND a
// * genuine one identically — and the "security test" passes while every upgrade
// * on earth fails closed for the wrong reason. Only the pair says the verifier
// * discriminates.
func TestVerifyAcceptsASignedBlobAndRejectsATamperedOne(t *testing.T) {
	key := testKey(t)
	// * Shaped like the real thing: gzip's magic bytes, then payload.
	blob := append([]byte{0x1f, 0x8b, 0x08}, []byte("pulse_1.1.0_darwin_arm64 archive contents")...)
	sig := signBlob(t, key, blob)

	if err := Verify(&key.PublicKey, blob, sig); err != nil {
		t.Fatalf("a correctly signed archive was rejected: %v\n"+
			"every upgrade would fail closed, and the rejection test below would still pass", err)
	}

	// * One byte, in the middle — the smallest change a substituted binary could
	// * possibly have.
	tampered := make([]byte, len(blob))
	copy(tampered, blob)
	tampered[len(tampered)/2] ^= 0x01

	err := Verify(&key.PublicKey, tampered, sig)
	if err == nil {
		t.Fatal("a modified archive verified against the untouched signature — the CLI would " +
			"install a binary nobody signed")
	}
	if !errors.Is(err, ErrBadSignature) {
		t.Errorf("rejection error = %v, want it to wrap ErrBadSignature so callers can name the cause", err)
	}
}

// * The signature is the other input that can be swapped, and swapping it is
// * cheaper for an attacker than forging one: publish an archive alongside a
// * signature made with your own key.
func TestVerifyRejectsASignatureFromADifferentKey(t *testing.T) {
	release, attacker := testKey(t), testKey(t)
	blob := []byte("a release archive")

	if err := Verify(&release.PublicKey, blob, signBlob(t, attacker, blob)); err == nil {
		t.Fatal("a signature made with a different key was accepted")
	}
}

// * cosign writes base64 TEXT. Handing raw DER (or anything else that is not
// * base64) to the verifier has to be a refusal, not a panic and not a pass.
func TestVerifyRejectsMalformedSignatures(t *testing.T) {
	key := testKey(t)
	blob := []byte("a release archive")
	digest := sha256.Sum256(blob)
	der, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("signing: %v", err)
	}

	for name, sig := range map[string][]byte{
		"empty":              {},
		"not base64":         []byte("!!!! not base64 !!!!"),
		"raw DER, undecoded": der,
		"base64 of garbage":  []byte(base64.StdEncoding.EncodeToString([]byte("not a signature"))),
	} {
		if err := Verify(&key.PublicKey, blob, sig); err == nil {
			t.Errorf("%s: accepted as a valid signature", name)
		}
	}
}

// * Whitespace is not tampering. The .sig files are published with a trailing
// * newline, and a verifier that fails on one rejects every real release.
func TestVerifyToleratesSurroundingWhitespace(t *testing.T) {
	key := testKey(t)
	blob := []byte("a release archive")
	sig := signBlob(t, key, blob)

	padded := append([]byte("\n  "), append(sig, []byte("  \n\n")...)...)
	if err := Verify(&key.PublicKey, blob, padded); err != nil {
		t.Fatalf("a signature with surrounding whitespace was rejected: %v", err)
	}
}

// * The key that verifies an upgrade is the key compiled into the binary. If it
// * does not parse, every upgrade fails — and it would fail at the moment of use,
// * on a user's machine, rather than here.
func TestEmbeddedKeyIsAUsableECDSAKey(t *testing.T) {
	pub, err := PublicKey()
	if err != nil {
		t.Fatalf("the embedded release key does not parse: %v", err)
	}
	if pub.Curve != elliptic.P256() {
		t.Errorf("embedded key curve = %v, want P-256 (what cosign generates and what signs our releases)",
			pub.Curve.Params().Name)
	}
}

// * The embedded copy must stay byte-identical to the cosign.pub at the
// * repository root — the file the README tells people to verify releases
// * against by hand, and the one the release notes link to on
// * raw.githubusercontent.com.
// *
// * If the signing key is ever rotated and only one of the two is updated, the
// * CLI and the documented manual check would trust different keys, and only one
// * of them would be right. This test is the reason the duplication is safe.
func TestEmbeddedKeyMatchesRepoRoot(t *testing.T) {
	root, err := os.ReadFile("../../cosign.pub")
	if err != nil {
		t.Fatalf("reading the repository's cosign.pub: %v", err)
	}
	if string(root) != string(cosignPub) {
		t.Error("internal/upgrade/cosign.pub has drifted from the cosign.pub at the repository root; " +
			"the CLI and the documented `cosign verify-blob` check would trust different keys")
	}
}

// * A non-ECDSA key has to be refused at parse time rather than reaching
// * VerifyASN1, which cannot use one.
func TestPublicKeyRejectsANonECDSAKey(t *testing.T) {
	original := cosignPub
	t.Cleanup(func() { cosignPub = original })

	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating an ed25519 key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(edPub)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	cosignPub = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	if _, err := PublicKey(); err == nil {
		t.Fatal("an ed25519 key was accepted as the release key; every verification would then fail " +
			"for a reason no error message names")
	}
}
