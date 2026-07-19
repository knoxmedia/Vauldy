package crypto

import (
	"bytes"
	"context"
	"errors"
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

// gatedEncryptReader exposes whether cancellation stops before a second source read.
type gatedEncryptReader struct {
	reads      int
	firstRead  chan struct{}
	allowFirst chan struct{}
}

func (r *gatedEncryptReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		close(r.firstRead)
		<-r.allowFirst
		for i := range p {
			p[i] = byte(i)
		}
		return len(p), nil
	}
	return 0, errors.New("source read after cancellation")
}

func TestEncryptFileContext_CancelStopsBeforeNextRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &gatedEncryptReader{firstRead: make(chan struct{}), allowFirst: make(chan struct{})}
	var dst bytes.Buffer
	done := make(chan error, 1)
	go func() { _, err := EncryptFileContext(ctx, r, &dst, bytes.Repeat([]byte{0x11}, 32)); done <- err }()
	<-r.firstRead
	cancel()
	close(r.allowFirst)
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if r.reads != 1 {
		t.Fatalf("reads=%d want 1", r.reads)
	}
}

func TestEncryptFileContext_PreCancelledWritesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var dst bytes.Buffer
	_, err := EncryptFileContext(ctx, bytes.NewReader([]byte("plain")), &dst, bytes.Repeat([]byte{0x11}, 32))
	if !errors.Is(err, context.Canceled) || dst.Len() != 0 {
		t.Fatalf("err=%v bytes=%d", err, dst.Len())
	}
}

type shortWriteAtCall struct {
	call      int
	shortCall int
}

func (w *shortWriteAtCall) Write(p []byte) (int, error) {
	w.call++
	if w.call == w.shortCall && len(p) > 0 {
		return len(p) - 1, nil
	}
	return len(p), nil
}

func TestEncryptFileReturnsErrShortWriteForHeader(t *testing.T) {
	_, err := EncryptFile(bytes.NewReader([]byte("payload")), &shortWriteAtCall{shortCall: 1}, bytes.Repeat([]byte{0x11}, 32))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error=%v want io.ErrShortWrite", err)
	}
}

func TestEncryptFileReturnsErrShortWriteForCiphertext(t *testing.T) {
	_, err := EncryptFile(bytes.NewReader([]byte("payload")), &shortWriteAtCall{shortCall: 2}, bytes.Repeat([]byte{0x11}, 32))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error=%v want io.ErrShortWrite", err)
	}
}
