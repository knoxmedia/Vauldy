package crypto

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestZeroReaderSeekSupportsServeContentRange(t *testing.T) {
	kek := bytes.Repeat([]byte{0x22}, 32)
	plain := bytes.Repeat([]byte{0xAB}, 4096)
	var enc bytes.Buffer
	res, err := EncryptFile(bytes.NewReader(plain), &enc, kek)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := DecryptStream(bytes.NewReader(enc.Bytes()), res.WrappedDEK, kek)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	rsc, ok := rc.(io.ReadSeekCloser)
	if !ok {
		t.Fatal("expected ReadSeekCloser")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Range", "bytes=100-199")
	rr := httptest.NewRecorder()
	http.ServeContent(rr, req, "clip.bin", time.Now(), rsc)

	if rr.Code != http.StatusPartialContent {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := rr.Body.Bytes()
	if len(got) != 100 {
		t.Fatalf("len=%d want 100", len(got))
	}
	for _, b := range got {
		if b != 0xAB {
			t.Fatalf("unexpected byte %x", b)
		}
	}
}

func TestPlaintextSize(t *testing.T) {
	kek := bytes.Repeat([]byte{0x33}, 32)
	plain := []byte("hello-plain")
	encPath := filepath.Join(t.TempDir(), "f.enc")
	f, err := os.Create(encPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EncryptFile(bytes.NewReader(plain), f, kek); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	size, err := PlaintextSize(encPath)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(plain)) {
		t.Fatalf("size=%d want %d", size, len(plain))
	}
}
