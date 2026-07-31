package crypto

import (
	"bytes"
	"context"
	"io"
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
