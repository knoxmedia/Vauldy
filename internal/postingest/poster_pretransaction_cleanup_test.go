package postingest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/storage"
)

func assertNoPosterObjectOrQuarantine(t *testing.T, upload string) {
	t.Helper()
	if got := posterObjectFiles(t, upload); len(got) != 0 {
		t.Fatalf("poster object or quarantine orphan=%v", got)
	}
}

func TestPosterCommitCleansNewSealOnPretransactionFailures(t *testing.T) {
	cases := []struct {
		name   string
		inject func(*testing.T, *sql.DB, string, Task, StagedPoster)
	}{
		{
			name: "media source query",
			inject: func(t *testing.T, _ *sql.DB, _ string, _ Task, _ StagedPoster) {
				original := posterLoadSourcePath
				posterLoadSourcePath = func(context.Context, *sql.DB, int64) (string, error) {
					return "", errors.New("injected media source query failure")
				}
				t.Cleanup(func() { posterLoadSourcePath = original })
			},
		},
		{
			name: "source fingerprint mismatch",
			inject: func(t *testing.T, db *sql.DB, _ string, task Task, _ StagedPoster) {
				original := posterAfterSealHook
				posterAfterSealHook = func() {
					source := taskSource(t, db, task.MediaID)
					if err := os.WriteFile(source, []byte("changed source after seal"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() { posterAfterSealHook = original })
			},
		},
		{
			name: "sealed artifact verification",
			inject: func(t *testing.T, _ *sql.DB, upload string, _ Task, staged StagedPoster) {
				original := posterAfterSealHook
				posterAfterSealHook = func() {
					final := storage.PosterObjectPath(upload, staged.Hash, ".jpg")
					if err := os.Chmod(final, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(final, []byte("mutated sealed poster"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() { posterAfterSealHook = original })
			},
		},
		{
			name: "source stat",
			inject: func(t *testing.T, _ *sql.DB, _ string, _ Task, _ StagedPoster) {
				original := posterSourceStat
				posterSourceStat = func(string) (os.FileInfo, error) {
					return nil, errors.New("injected source stat failure")
				}
				t.Cleanup(func() { posterSourceStat = original })
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, upload, task := seedCurrentLinkedPosterTask(t)
			runner := realPosterStageRunner(t, db, upload)
			staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
			if err != nil {
				t.Fatal(err)
			}
			tc.inject(t, db, upload, task, staged)
			if err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); err == nil {
				t.Fatal("expected pretransaction rejection")
			}
			assertNoPosterObjectOrQuarantine(t, upload)
		})
	}
}

func TestPosterCommitPretransactionFailureRetainsSharedCAS(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	final := storage.PosterObjectPath(upload, staged.Hash, ".jpg")
	if err = os.MkdirAll(filepath.Dir(final), 0755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(staged.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(final, body, 0444); err != nil {
		t.Fatal(err)
	}
	original := posterLoadSourcePath
	posterLoadSourcePath = func(context.Context, *sql.DB, int64) (string, error) {
		return "", errors.New("injected media source query failure")
	}
	t.Cleanup(func() { posterLoadSourcePath = original })

	if err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); err == nil {
		t.Fatal("expected pretransaction rejection")
	}
	if _, err = os.Stat(final); err != nil {
		t.Fatalf("shared CAS removed: %v", err)
	}
	quarantine := filepath.Join(upload, "posters", "objects", "sha256", "quarantine")
	if entries, readErr := os.ReadDir(quarantine); readErr == nil && len(entries) != 0 {
		t.Fatalf("quarantine orphan=%v", entries)
	}
}
