package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type recordingProbeOps struct {
	opened                                    string
	flags                                     int
	mode                                      os.FileMode
	wrote, synced, closed, removed, dirSynced bool
	failWrite                                 bool
}
type fakeProbeFile struct{ ops *recordingProbeOps }

func (f *fakeProbeFile) Write([]byte) (int, error) {
	f.ops.wrote = true
	if f.ops.failWrite {
		return 0, errors.New("read only")
	}
	return 1, nil
}
func (f *fakeProbeFile) Sync() error                            { f.ops.synced = true; return nil }
func (f *fakeProbeFile) Close() error                           { f.ops.closed = true; return nil }
func (o *recordingProbeOps) MkdirAll(string, os.FileMode) error { return nil }
func (o *recordingProbeOps) OpenFile(path string, flags int, mode os.FileMode) (artifactProbeFile, error) {
	o.opened = path
	o.flags = flags
	o.mode = mode
	return &fakeProbeFile{o}, nil
}
func (o *recordingProbeOps) Remove(string) error  { o.removed = true; return nil }
func (o *recordingProbeOps) SyncDir(string) error { o.dirSynced = true; return nil }

func TestProbeArtifactRootDurablyWritesAndCleans(t *testing.T) {
	o := &recordingProbeOps{}
	root := t.TempDir()
	if err := probeArtifactRoot(root, o); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(o.opened) != root || o.flags&(os.O_CREATE|os.O_EXCL|os.O_WRONLY) != (os.O_CREATE|os.O_EXCL|os.O_WRONLY) || o.mode != 0600 || !o.wrote || !o.synced || !o.closed || !o.removed || !o.dirSynced {
		t.Fatalf("ops=%+v", o)
	}
}
func TestProbeArtifactRootFailureCleansResidue(t *testing.T) {
	o := &recordingProbeOps{failWrite: true}
	if err := probeArtifactRoot(t.TempDir(), o); err == nil {
		t.Fatal("expected failure")
	}
	if !o.closed || !o.removed {
		t.Fatalf("ops=%+v", o)
	}
}

func TestProbeArtifactRootCreatesAbsentRootWithoutResidue(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing", "root")
	if err := probeArtifactRoot(root, osArtifactProbeOps{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe residue=%v", entries)
	}
}
