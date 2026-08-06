package retirement

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/publication"
	"knox-media/internal/storage"
)

func imoDigest(t *testing.T, fp string) string {
	t.Helper()
	idx := strings.LastIndex(fp, "|imohash:")
	if idx < 0 {
		t.Fatalf("not an imohash fingerprint: %q", fp)
	}
	return fp[idx+len("|imohash:"):]
}

// TestImoHashFingerprintIdenticalFilesEqual proves the sampled imohash
// fingerprint is deterministic: re-hashing the same file and hashing a
// byte-identical file at a different path yield the same digest.
func TestImoHashFingerprintIdenticalFilesEqual(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	// Larger than the 48KiB imohash sample window so the middle sample is real.
	payload := make([]byte, 200*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	if err := os.WriteFile(a, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, payload, 0600); err != nil {
		t.Fatal(err)
	}

	fpA, err := fingerprintFile(a)
	if err != nil {
		t.Fatal(err)
	}
	fpA2, err := fingerprintFile(a)
	if err != nil {
		t.Fatal(err)
	}
	if fpA != fpA2 {
		t.Fatalf("re-hashing the same file changed the fingerprint: %q vs %q", fpA, fpA2)
	}
	fpB, err := fingerprintFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if imoDigest(t, fpA) != imoDigest(t, fpB) {
		t.Fatalf("identical files produced different imohash digests: %q vs %q", fpA, fpB)
	}
}

// TestImoHashFingerprintDifferentSizesDiffer proves files with different sizes
// (and content) yield different imohash digests, so uniqueness is preserved.
func TestImoHashFingerprintDifferentSizesDiffer(t *testing.T) {
	dir := t.TempDir()
	large := filepath.Join(dir, "large.bin")
	small := filepath.Join(dir, "small.bin")
	big := make([]byte, 100*1024)
	for i := range big {
		big[i] = byte(i % 253)
	}
	if err := os.WriteFile(large, big, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(small, big[:64*1024], 0600); err != nil {
		t.Fatal(err)
	}
	fpLarge, err := fingerprintFile(large)
	if err != nil {
		t.Fatal(err)
	}
	fpSmall, err := fingerprintFile(small)
	if err != nil {
		t.Fatal(err)
	}
	if imoDigest(t, fpLarge) == imoDigest(t, fpSmall) {
		t.Fatalf("different-sized files produced the same imohash digest: %q", fpLarge)
	}
}

// TestFingerprintMatchesAcceptsLegacySHA256 proves the compatibility path:
// stored sha256:-format fingerprints (written before the imohash migration)
// are verified against a live file using a full-file SHA-256, while malformed
// or mismatched values fail closed.
func TestFingerprintMatchesAcceptsLegacySHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.bin")
	if err := os.WriteFile(path, []byte("legacy rows must keep passing the fence"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := fileSHA256Ctx(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	legacyFP := fmt.Sprintf("%s|%d|%d|sha256:%s", filepath.Clean(abs), info.Size(), info.ModTime().UnixNano(), sha)

	ok, err := fingerprintMatches(context.Background(), legacyFP, path)
	if err != nil || !ok {
		t.Fatalf("legacy sha256 fingerprint must match the live file: ok=%v err=%v", ok, err)
	}
	ok, err = fingerprintMatches(context.Background(), legacyFP+"dead", path)
	if err != nil {
		t.Fatalf("mismatched legacy sha256 must not error: %v", err)
	}
	if ok {
		t.Fatal("mismatched legacy sha256 fingerprint unexpectedly matched")
	}

	// An unrecognized stored value fails closed with an error.
	if _, err = fingerprintMatches(context.Background(), "not-a-fingerprint", path); err == nil {
		t.Fatal("unrecognized stored fingerprint must fail closed")
	}
}

// TestCanonicalFingerprintInterchangeability proves the storage and publication
// canonical generators emit the identical imohash fingerprint for the same file,
// so test fixtures and runtime comparisons stay interchangeable.
func TestCanonicalFingerprintInterchangeability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interchangeable.bin")
	if err := os.WriteFile(path, []byte("fixtures and runtime must agree"), 0600); err != nil {
		t.Fatal(err)
	}
	storageFP, err := storage.EncryptionSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	publicationFP, err := publication.SourceFingerprintContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if storageFP != publicationFP {
		t.Fatalf("canonical fingerprints diverge: storage=%q publication=%q", storageFP, publicationFP)
	}
	ok, err := fingerprintMatches(context.Background(), storageFP, path)
	if err != nil || !ok {
		t.Fatalf("imohash fingerprint must match the live file: ok=%v err=%v", ok, err)
	}
}
