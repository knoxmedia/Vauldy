//go:build ignore

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"knox-media/internal/config"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

func main() {
	projectRoot, _ := filepath.Abs(".")
	cfg, _ := config.Load(filepath.Join(projectRoot, "config.yml"))
	cfg.ResolveExecutablePaths(projectRoot)
	db, _ := store.OpenSQLite(cfg.Data.DB)
	defer db.Close()
	mainKey := os.Getenv("KNOX_MAIN_KEY")
	if mainKey == "" {
		mainKey = cfg.Security.JWTSecret
	}
	vault, _ := keystore.NewVault(mainKey, cfg.EncryptedAssetsKEKSaltPath())

	var filePath string
	var mediaID int64 = 3829
	_ = db.QueryRow(`SELECT file_path FROM media WHERE id=?`, mediaID).Scan(&filePath)

	plain := storage.ResolveKeyframeProbePath(db, mediaID, filePath)
	fmt.Println("catalog:", filePath)
	fmt.Println("plain:  ", plain)

	tests := []struct {
		name string
		args []string
		in   string
		stdin io.Reader
		cleanup func()
	}{
		{
			name: "enc-pipe-start",
			args: []string{"-hide_banner", "-loglevel", "error", "-t", "3", "-i", "pipe:0", "-f", "null", "-"},
		},
		{
			name: "plain-file",
			args: []string{"-hide_banner", "-loglevel", "error", "-t", "3", "-i", plain, "-f", "null", "-"},
		},
	}

	in, err := storage.OpenFFmpegInput(db, vault, mediaID, filePath, 0)
	if err != nil {
		panic(err)
	}
	tests[0].stdin = in.Stdin
	tests[0].cleanup = in.Cleanup

	for _, tc := range tests {
		args := tc.args
		if tc.name == "plain-file" {
			args[6] = plain
		}
		cmd := exec.CommandContext(context.Background(), cfg.FFmpeg.FFmpegPath, args...)
		if tc.stdin != nil {
			cmd.Stdin = tc.stdin
		}
		out, err := cmd.CombinedOutput()
		if tc.cleanup != nil {
			tc.cleanup()
		}
		fmt.Printf("\n=== %s err=%v ===\n%s\n", tc.name, err, string(out))
		if tc.name == "enc-pipe-start" {
			// reopen pipe for next tests if needed
			time.Sleep(100 * time.Millisecond)
		}
	}
}
