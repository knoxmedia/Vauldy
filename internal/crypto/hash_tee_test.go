package crypto

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
)

func TestEncryptResumeSessionSumMatchesEncryptedEnvelope(t *testing.T) {
	kek := bytes.Repeat([]byte{0x77}, 32)
	plain := bytes.Repeat([]byte("ciphertext-hash-"), 4096)

	session, err := BeginEncryptResume(kek)
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := session.WriteHeader(&encrypted); err != nil {
		t.Fatal(err)
	}
	if err := session.EncryptRange(context.Background(), bytes.NewReader(plain), &encrypted, 0, int64(len(plain))); err != nil {
		t.Fatal(err)
	}

	want := sha256.Sum256(encrypted.Bytes())
	if got := session.Sum(); !bytes.Equal(got, want[:]) {
		t.Fatalf("ciphertext hash mismatch: got %x want %x", got, want)
	}
}

func TestRestoredEncryptResumeSessionSumMatchesEncryptedEnvelope(t *testing.T) {
	kek := bytes.Repeat([]byte{0x88}, 32)
	plain := bytes.Repeat([]byte{0xA5}, 2<<20)
	const split = 1 << 20

	first, err := BeginEncryptResume(kek)
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := first.WriteHeader(&encrypted); err != nil {
		t.Fatal(err)
	}
	if err := first.EncryptRange(context.Background(), bytes.NewReader(plain[:split]), &encrypted, 0, split); err != nil {
		t.Fatal(err)
	}

	result := first.Result()
	restored, err := RestoreEncryptResume(kek, result.WrappedDEK, result.IV)
	if err != nil {
		t.Fatal(err)
	}
	prefix := append([]byte(nil), encrypted.Bytes()...)
	if err := restored.HashPrefix(bytes.NewReader(prefix), int64(len(prefix))); err != nil {
		t.Fatal(err)
	}
	if err := restored.EncryptRange(context.Background(), bytes.NewReader(plain[split:]), &encrypted, split, int64(len(plain)-split)); err != nil {
		t.Fatal(err)
	}

	want := sha256.Sum256(encrypted.Bytes())
	if got := restored.Sum(); !bytes.Equal(got, want[:]) {
		t.Fatalf("resumed ciphertext hash mismatch: got %x want %x", got, want)
	}
}
