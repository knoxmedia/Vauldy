package crypto

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestEncryptFileContextWithResume_TwoPhasesMatchFull(t *testing.T) {
	kek := bytes.Repeat([]byte{0x22}, 32)
	plain := bytes.Repeat([]byte{0xAB}, 3<<20) // 3MiB
	var out bytes.Buffer
	st, err := BeginEncryptResume(kek)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteHeader(&out); err != nil {
		t.Fatal(err)
	}
	n1 := int64(1 << 20)
	if err := st.EncryptRange(context.Background(), bytes.NewReader(plain[:n1]), &out, 0, n1); err != nil {
		t.Fatal(err)
	}
	if err := st.EncryptRange(context.Background(), bytes.NewReader(plain[n1:]), &out, n1, int64(len(plain))-n1); err != nil {
		t.Fatal(err)
	}
	got := st.Result()
	dec, err := DecryptStream(bytes.NewReader(out.Bytes()), got.WrappedDEK, kek)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	body, _ := io.ReadAll(dec)
	if !bytes.Equal(body, plain) {
		t.Fatalf("resume decrypt mismatch len=%d", len(body))
	}
}

func TestRestoreEncryptResume_ContinuesAfterRestart(t *testing.T) {
	kek := bytes.Repeat([]byte{0x33}, 32)
	plain := bytes.Repeat([]byte{0xCD}, 3<<20) // 3MiB
	var out bytes.Buffer
	st, err := BeginEncryptResume(kek)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteHeader(&out); err != nil {
		t.Fatal(err)
	}
	n1 := int64(1 << 20)
	if err := st.EncryptRange(context.Background(), bytes.NewReader(plain[:n1]), &out, 0, n1); err != nil {
		t.Fatal(err)
	}
	first := st.Result()
	wrapped := append([]byte(nil), first.WrappedDEK...)
	iv := append([]byte(nil), first.IV...)

	st2, err := RestoreEncryptResume(kek, wrapped, iv)
	if err != nil {
		t.Fatal(err)
	}
	if err := st2.EncryptRange(context.Background(), bytes.NewReader(plain[n1:]), &out, n1, int64(len(plain))-n1); err != nil {
		t.Fatal(err)
	}
	got := st2.Result()
	dec, err := DecryptStream(bytes.NewReader(out.Bytes()), got.WrappedDEK, kek)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	body, _ := io.ReadAll(dec)
	if !bytes.Equal(body, plain) {
		t.Fatalf("restore resume decrypt mismatch len=%d", len(body))
	}
}

func TestEncryptRange_ShortReadReturnsError(t *testing.T) {
	kek := bytes.Repeat([]byte{0x44}, 32)
	st, err := BeginEncryptResume(kek)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	src := bytes.NewReader(make([]byte, 32))
	err = st.EncryptRange(context.Background(), src, &out, 0, 64)
	if err == nil {
		t.Fatal("expected error for short read")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !strings.Contains(err.Error(), "short read") {
		t.Fatalf("err=%v", err)
	}
}

func TestEncryptRange_NegativePlainLenRejected(t *testing.T) {
	kek := bytes.Repeat([]byte{0x55}, 32)
	st, err := BeginEncryptResume(kek)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = st.EncryptRange(context.Background(), bytes.NewReader([]byte{0x01}), &out, 0, -1)
	if err == nil {
		t.Fatal("expected error for negative plainLen")
	}
	if !strings.Contains(err.Error(), "plainLen") {
		t.Fatalf("err=%v", err)
	}
}

func TestEncryptRange_MisalignedOffsetRejected(t *testing.T) {
	kek := bytes.Repeat([]byte{0x66}, 32)
	st, err := BeginEncryptResume(kek)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = st.EncryptRange(context.Background(), bytes.NewReader(make([]byte, 32)), &out, 1, 32)
	if err == nil {
		t.Fatal("expected error for misaligned plainOffset")
	}
	if !strings.Contains(err.Error(), "block aligned") {
		t.Fatalf("err=%v", err)
	}
}
