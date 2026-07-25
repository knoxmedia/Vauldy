package publication

import (
	"errors"
	"reflect"
	"testing"
)

type fakeEncryptedLibraryRows struct {
	values            []EncryptedLibrary
	index             int
	iterErr, closeErr error
}

func (r *fakeEncryptedLibraryRows) Next() bool { return r.index < len(r.values) }
func (r *fakeEncryptedLibraryRows) Scan(dest ...any) error {
	lib := r.values[r.index]
	r.index++
	*(dest[0].(*int64)) = lib.ID
	*(dest[1].(*string)) = lib.Path
	*(dest[2].(*string)) = lib.Mode
	return nil
}
func (r *fakeEncryptedLibraryRows) Err() error   { return r.iterErr }
func (r *fakeEncryptedLibraryRows) Close() error { return r.closeErr }
func TestCollectEncryptedLibrariesJoinsIterationAndCloseErrors(t *testing.T) {
	iter := errors.New("iterate")
	closeErr := errors.New("close")
	_, err := collectEncryptedLibraries(&fakeEncryptedLibraryRows{values: []EncryptedLibrary{{ID: 1, Path: "x", Mode: "data"}}, iterErr: iter, closeErr: closeErr})
	if !errors.Is(err, iter) || !errors.Is(err, closeErr) {
		t.Fatalf("err=%v", err)
	}
}
func TestCollectEncryptedLibrariesReturnsRows(t *testing.T) {
	want := []EncryptedLibrary{{ID: 1, Path: "a", Mode: "library"}, {ID: 2, Path: "b", Mode: "data"}}
	got, err := collectEncryptedLibraries(&fakeEncryptedLibraryRows{values: want})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v err=%v", got, err)
	}
}
