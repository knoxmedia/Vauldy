package crypto

import (
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCTRStreamingEncryptDecryptLargeFile(t *testing.T) {
	kek := make([]byte, 32)
	_, _ = rand.Read(kek)

	const plainSize = 5 * 1024 * 1024
	dir := t.TempDir()
	plainPath := filepath.Join(dir, "plain.bin")
	encPath := filepath.Join(dir, "plain.enc")

	plainF, err := os.Create(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1024*1024)
	for written := 0; written < plainSize; {
		_, _ = rand.Read(chunk)
		n := plainSize - written
		if n > len(chunk) {
			n = len(chunk)
		}
		if _, err := plainF.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
		written += n
	}
	_ = plainF.Close()

	plainIn, err := os.Open(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	encOut, err := os.Create(encPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := EncryptFile(plainIn, encOut, kek)
	_ = plainIn.Close()
	_ = encOut.Close()
	if err != nil {
		t.Fatal(err)
	}

	size, err := PlaintextSize(encPath)
	if err != nil || size != plainSize {
		t.Fatalf("PlaintextSize=%d err=%v want %d", size, err, plainSize)
	}

	rc, err := OpenDecryptSeeker(encPath, res.WrappedDEK, kek)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	if _, err := rc.Seek(plainSize-512, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	tail := make([]byte, 512)
	if _, err := io.ReadFull(rc, tail); err != nil {
		t.Fatal(err)
	}
	plainTail, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tail, plainTail[plainSize-512:]) {
		t.Fatal("tail mismatch after seek")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Range", "bytes=1048576-1048675")
	rr := httptest.NewRecorder()
	if _, err := rc.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	http.ServeContent(rr, req, "big.bin", time.Now(), rc)
	if rr.Code != http.StatusPartialContent {
		t.Fatalf("status=%d", rr.Code)
	}
	if rr.Body.Len() != 100 {
		t.Fatalf("range len=%d", rr.Body.Len())
	}
}
