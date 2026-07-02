package crypto

import (
	"bytes"
	"io"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	kek := bytes.Repeat([]byte{0x11}, 32)
	plain := []byte("knox-media-9527-envelope-test")
	var enc bytes.Buffer
	res, err := EncryptFile(bytes.NewReader(plain), &enc, kek)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := DecryptStream(bytes.NewReader(enc.Bytes()), res.WrappedDEK, kek)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	got, err := io.ReadAll(dec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q want %q", got, plain)
	}
}
