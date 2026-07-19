package atomicfile

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteFileConcurrentReadersSeeOnlyCompleteVersions(t *testing.T) {
	target := filepath.Join(t.TempDir(), "face.jpg")
	a := bytes.Repeat([]byte("a"), 8192)
	b := bytes.Repeat([]byte("b"), 8192)
	if err := WriteFile(context.Background(), target, a, 0o644); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				data, err := ReadFile(target)
				if err != nil {
					select {
					case errCh <- err:
					default:
						{
						}
					}
					return
				}
				if !bytes.Equal(data, a) && !bytes.Equal(data, b) {
					select {
					case errCh <- os.ErrInvalid:
					default:
						{
						}
					}
					return
				}
			}
		}
	}()
	for i := 0; i < 50; i++ {
		data := a
		if i%2 == 1 {
			data = b
		}
		if err := WriteFile(context.Background(), target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestStageRollbackRemovesPublishedNewTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "new.jpg")
	staged, err := Stage(context.Background(), target, []byte("jpeg"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err = staged.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(target); err != nil {
		t.Fatal(err)
	}
	staged.Rollback()
	if _, err = os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target remains: %v", err)
	}
}
