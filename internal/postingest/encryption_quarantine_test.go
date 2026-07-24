package postingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuarantinePlaintextMovesSourceUnderRestrictedRootAndRestores(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "photo.jpg")
	payload := []byte("plaintext-photo")
	if err := os.WriteFile(source, payload, 0644); err != nil {
		t.Fatal(err)
	}
	q, err := quarantinePlaintext(source, root, 41, 2, "stage-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still public: %v", err)
	}
	got, err := os.ReadFile(q)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("quarantine=%q err=%v", got, err)
	}
	if err = restoreQuarantinedPlaintext(q, source, root); err != nil {
		t.Fatal(err)
	}
	if got, err = os.ReadFile(source); err != nil || string(got) != string(payload) {
		t.Fatalf("restored=%q err=%v", got, err)
	}
}

func TestSafeEncryptionStageIDRejectsPathComponents(t *testing.T) {
	for _, id := range []string{"../escape", "stage/child", "selected-1-1", "", "00000000-0000-0000-0000-000000000000"} {
		got := safeEncryptionStageID(id)
		want := id == "00000000-0000-0000-0000-000000000000"
		if got != want {
			t.Fatalf("safe(%q)=%v want %v", id, got, want)
		}
	}
}
