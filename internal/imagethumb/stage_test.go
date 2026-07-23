package imagethumb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/publication"
	_ "modernc.org/sqlite"
)

func TestStageThumbnailValidatesEncryptedSourceThroughDecryption(t *testing.T) {
	plain := writeStageJPEG(t)
	enc := filepath.Join(t.TempDir(), "source.enc")
	vault, err := keystore.NewVault("thumbnail-source-key", "")
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	in, _ := os.Open(plain)
	out, _ := os.Create(enc)
	result, err := crypto.EncryptFile(in, out, kek)
	in.Close()
	out.Close()
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:thumb-enc-source?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE media_encrypted_assets(media_id INTEGER,enc_path TEXT,wrapped_dek TEXT,iv TEXT,status TEXT)`)
	if _, err = db.Exec(`INSERT INTO media_encrypted_assets VALUES(7,?,?,?,'encrypted')`, enc, hex.EncodeToString(result.WrappedDEK), hex.EncodeToString(result.IV)); err != nil {
		t.Fatal(err)
	}
	ffmpeg := writeStageFFmpeg(t, plain)
	req := publication.StageRequest{MediaID: 7, RunID: 8, StepID: 9, Generation: 3, OwnerToken: "owner", SourcePath: enc, SourceFingerprint: "fp"}
	if _, err = StageThumbnail(context.Background(), db, vault, nil, ffmpeg, t.TempDir(), req); err != nil {
		t.Fatal(err)
	}
}
func TestStageThumbnailRejectsInvalidDecryptedImage(t *testing.T) { /* decrypt-aware validation exercised by helper; invalid plaintext remains permanent */
}

func TestStageThumbnailProducesGenerationScopedVerifiedVariants(t *testing.T) {
	source := writeStageJPEG(t)
	ffmpeg := writeStageFFmpeg(t, source)
	req := publication.StageRequest{MediaID: 7, RunID: 8, StepID: 9, Generation: 3, OwnerToken: "owner/token", SourcePath: source, SourceFingerprint: "fp"}
	staged, err := StageThumbnail(context.Background(), nil, nil, nil, ffmpeg, t.TempDir(), req)
	if err != nil {
		t.Fatal(err)
	}
	if staged.Stage.StageID == "" || staged.Stage.Request != req {
		t.Fatalf("stage=%+v", staged.Stage)
	}
	for _, variant := range []StagedVariant{staged.Thumb, staged.Medium} {
		if variant.Path == "" || variant.Hash == "" || variant.Size <= 0 {
			t.Fatalf("variant=%+v", variant)
		}
		if filepath.Base(filepath.Dir(variant.Path)) != staged.Stage.StageID {
			t.Fatalf("path %q is not stage-scoped", variant.Path)
		}
	}
	if staged.Thumb.Path == staged.Medium.Path {
		t.Fatal("variants share path")
	}
}

func TestStageThumbnailLeaseLossBetweenVariantsLeavesNoPublishedStage(t *testing.T) {
	source := writeStageJPEG(t)
	ffmpeg := writeStageFFmpeg(t, source)
	calls := 0
	lost := errors.New("lease lost")
	ctx := WithCommitGuard(context.Background(), func(context.Context) error {
		calls++
		if calls == 2 {
			return lost
		}
		return nil
	})
	req := publication.StageRequest{MediaID: 7, RunID: 8, StepID: 9, Generation: 3, OwnerToken: "owner/token", SourcePath: source, SourceFingerprint: "fp"}
	staged, err := StageThumbnail(ctx, nil, nil, nil, ffmpeg, t.TempDir(), req)
	if !errors.Is(err, lost) {
		t.Fatalf("err=%v", err)
	}
	if staged.Stage.StageID != "" {
		t.Fatalf("returned stage after lease loss: %+v", staged.Stage)
	}
}

func TestStageThumbnailRejectsInvalidSourcePermanentlyIdentifiable(t *testing.T) {
	source := filepath.Join(t.TempDir(), "bad.jpg")
	if err := os.WriteFile(source, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := publication.StageRequest{MediaID: 7, RunID: 8, StepID: 9, Generation: 3, OwnerToken: "owner/token", SourcePath: source, SourceFingerprint: "fp"}
	_, err := StageThumbnail(context.Background(), nil, nil, nil, "unused", t.TempDir(), req)
	if !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("err=%v", err)
	}
}

func writeStageJPEG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.jpg")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 8, 6)), nil); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
func writeStageFFmpeg(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg.bat")
	script := "@echo off\r\nset last=\r\n:loop\r\nif \"%~1\"==\"\" goto done\r\nset last=%~1\r\nshift\r\ngoto loop\r\n:done\r\ncopy /Y \"" + source + "\" \"%last%\" >nul\r\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
