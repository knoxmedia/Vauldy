package postingest

import (
	"context"
	"errors"

	"database/sql"

	"knox-media/internal/store"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestQuarantinePlaintextMovesSourceUnderRestrictedRootAndRestores(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "photo.jpg")
	payload := []byte("plaintext-photo")
	if err := os.WriteFile(source, payload, 0644); err != nil {
		t.Fatal(err)
	}
	q, err := quarantinePlaintext(source, root, 41, 2, "00000000-0000-0000-0000-000000000001")
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
	if err = restoreQuarantinedPlaintext(q, source, root, 41, 2, "00000000-0000-0000-0000-000000000001"); err != nil {
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

func TestEncryptionStateMachineSeamsArePerExecution(t *testing.T) {
	first, second := 0, 0
	a := EncryptionStateMachineSeams{BeforeMove: func() error { first++; return nil }}
	b := EncryptionStateMachineSeams{BeforeMove: func() error { second++; return nil }}
	if err := a.BeforeMove(); err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("first=%d second=%d", first, second)
	}
	if err := b.BeforeMove(); err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 1 {
		t.Fatalf("first=%d second=%d", first, second)
	}
}

func TestEncryptionStateMachineHooksEachPhase(t *testing.T) {
	phases := []struct {
		name string
		set  func(*EncryptionStateMachineSeams)
	}{
		{"before_move", func(s *EncryptionStateMachineSeams) { s.BeforeMove = func() error { return os.ErrPermission } }},
		{"after_move", func(s *EncryptionStateMachineSeams) { s.AfterMove = func() error { return os.ErrPermission } }},
		{"before_mark_quarantined", func(s *EncryptionStateMachineSeams) {
			s.BeforeMarkQuarantined = func() error { return os.ErrPermission }
		}},
		{"before_final_commit", func(s *EncryptionStateMachineSeams) { s.BeforeFinalCommit = func() error { return os.ErrPermission } }},
		{"immediate_tx", func(s *EncryptionStateMachineSeams) {
			s.ImmediateTx = func(context.Context, *sql.DB, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
				return store.ImmediateOutcome{}, os.ErrPermission
			}
		}},
	}
	for _, phase := range phases {
		t.Run(phase.name, func(t *testing.T) {
			var s EncryptionStateMachineSeams
			phase.set(&s)
			if phase.name == "before_final_commit" {
				if s.BeforeFinalCommit == nil || s.BeforeFinalCommit() == nil {
					t.Fatal("hook not injected")
				}
			} else if phase.name == "immediate_tx" {
				if _, e := s.immediate(context.Background(), nil, nil); e == nil {
					t.Fatal("tx seam not injected")
				}
			} else {
				var h func() error
				switch phase.name {
				case "before_move":
					h = s.BeforeMove
				case "after_move":
					h = s.AfterMove
				default:
					h = s.BeforeMarkQuarantined
				}
				if h == nil || h() == nil {
					t.Fatal("phase hook not injected")
				}
			}
		})
	}
}

func TestQuarantinePlaintextRefusesCrossVolumeCopyOnEXDEV(t *testing.T) {
	root, source := t.TempDir(), filepath.Join(t.TempDir(), "photo.jpg")
	payload := []byte("plaintext-must-not-copy")
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	exdev := &os.LinkError{Op: "rename", Old: source, New: "target", Err: syscall.EXDEV}
	ops := defaultEncryptionFileOps()
	ops.rename = func(oldpath, newpath string) error { return exdev }
	ops.syncFile = func(*os.File) error { t.Fatal("syncFile must not run during refused cross-volume copy"); return nil }
	q, err := quarantinePlaintextWithOps(source, root, 1, 1, "00000000-0000-0000-0000-000000000099", ops)
	if err == nil || !strings.Contains(err.Error(), "refuses cross-volume") {
		t.Fatalf("want refuses cross-volume error, got path=%q err=%v", q, err)
	}
	if !errors.Is(err, syscall.EXDEV) {
		t.Fatalf("want EXDEV wrapped: %v", err)
	}
	got, readErr := os.ReadFile(source)
	if readErr != nil || string(got) != string(payload) {
		t.Fatalf("source must remain unchanged: %q err=%v", got, readErr)
	}
	stageDir := filepath.Join(root, "1", "1", "00000000-0000-0000-0000-000000000099")
	target := filepath.Join(stageDir, "source")
	tmp := target + ".tmp"
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("quarantine target must not exist after refused copy: %v", statErr)
	}
	if _, statErr := os.Stat(tmp); !os.IsNotExist(statErr) {
		t.Fatalf("quarantine temp must not remain after refused copy: %v", statErr)
	}
}

func TestQuarantinePlaintextSyncsBeforeReturning(t *testing.T) {
	root, source := t.TempDir(), filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(source, []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	var events []string
	ops := encryptionFileOps{
		syncFile: func(*os.File) error { events = append(events, "file"); return nil },
		syncDir:  func(string) error { events = append(events, "dir"); return nil },
	}
	if _, err := quarantinePlaintextWithOps(source, root, 1, 1, "00000000-0000-0000-0000-000000000001", ops); err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[len(events)-1] != "dir" {
		t.Fatalf("durability order=%v", events)
	}
}

func TestQuarantinePlaintextDirectorySyncFailureIsRecoverable(t *testing.T) {
	root, source := t.TempDir(), filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(source, []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	want := errors.New("injected directory sync failure")
	ops := defaultEncryptionFileOps()
	ops.syncDir = func(string) error { return want }
	q, err := quarantinePlaintextWithOps(source, root, 1, 1, "00000000-0000-0000-0000-000000000002", ops)
	if !errors.Is(err, want) {
		t.Fatalf("path=%q err=%v", q, err)
	}
	if _, sourceErr := os.Stat(source); sourceErr != nil {
		if _, quarantineErr := os.Stat(filepath.Join(root, "1", "1", "00000000-0000-0000-0000-000000000002", "source")); quarantineErr != nil {
			t.Fatalf("neither recoverable copy exists: source=%v quarantine=%v", sourceErr, quarantineErr)
		}
	}
}

func TestRestoreQuarantinedPlaintextRejectsWrongJournalLayout(t *testing.T) {
	root := t.TempDir()
	stage := "00000000-0000-0000-0000-000000000010"
	wrong := filepath.Join(root, "2", "3", stage, "source")
	if err := os.MkdirAll(filepath.Dir(wrong), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrong, []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.jpg")
	if err := restoreQuarantinedPlaintext(wrong, destination, root, 1, 3, stage); err == nil {
		t.Fatal("wrong media identity accepted")
	}
	if _, err := os.Stat(wrong); err != nil {
		t.Fatalf("quarantine changed: %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination changed: %v", err)
	}
}

func TestRestoreQuarantinedPlaintextRejectsIntermediateSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	stage := "00000000-0000-0000-0000-000000000011"
	if err := os.MkdirAll(filepath.Join(root, "1"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "1", "2")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	external := filepath.Join(outside, stage, "source")
	if err := os.MkdirAll(filepath.Dir(external), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(root, "1", "2", stage, "source")
	destination := filepath.Join(t.TempDir(), "restored.jpg")
	if err := restoreQuarantinedPlaintext(quarantine, destination, root, 1, 2, stage); err == nil {
		t.Fatal("intermediate symlink accepted")
	}
	if got, err := os.ReadFile(external); err != nil || string(got) != "external" {
		t.Fatalf("external changed: %q %v", got, err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination changed: %v", err)
	}
}

func TestRestoreQuarantinedPlaintextRejectsFinalSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	stage := "00000000-0000-0000-0000-000000000012"
	quarantine := filepath.Join(root, "1", "2", stage, "source")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(outside, "external")
	if err := os.WriteFile(external, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, quarantine); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.jpg")
	if err := restoreQuarantinedPlaintext(quarantine, destination, root, 1, 2, stage); err == nil {
		t.Fatal("final symlink accepted")
	}
	if got, err := os.ReadFile(external); err != nil || string(got) != "external" {
		t.Fatalf("external changed: %q %v", got, err)
	}
}

func TestValidateQuarantinePathUsesInjectableLstat(t *testing.T) {
	root := t.TempDir()
	stage := "00000000-0000-0000-0000-000000000013"
	quarantine := filepath.Join(root, "1", "2", stage, "source")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantine, []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	original := encryptionLstat
	encryptionLstat = func(path string) (os.FileInfo, error) {
		info, err := os.Lstat(path)
		if err == nil && sameQuarantinePath(path, filepath.Join(root, "1", "2")) {
			return symlinkFileInfo{FileInfo: info}, nil
		}
		return info, err
	}
	t.Cleanup(func() { encryptionLstat = original })
	if _, err := validateExistingQuarantinePath(root, quarantine, 1, 2, stage); err == nil {
		t.Fatal("simulated reparse/symlink component accepted")
	}
}

type symlinkFileInfo struct{ os.FileInfo }

func (s symlinkFileInfo) Mode() os.FileMode { return s.FileInfo.Mode() | os.ModeSymlink }
