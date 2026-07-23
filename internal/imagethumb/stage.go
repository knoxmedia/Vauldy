package imagethumb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"knox-media/internal/keystore"
	"knox-media/internal/publication"
	"knox-media/internal/storage"
)

var ErrInvalidImage = errors.New("invalid image")

type StagedVariant struct {
	Kind, LogicalName, Path, Hash string
	Size                          int64
	Derived                       *storage.StagedDerivedAsset
}
type StagedThumbnail struct {
	Stage         publication.StageRecord
	Thumb, Medium StagedVariant
}

func StageThumbnail(ctx context.Context, db *sql.DB, vault *keystore.Vault, derived *storage.DerivedAssetStore, ffmpegPath, baseDir string, req publication.StageRequest) (StagedThumbnail, error) {
	if req.MediaID <= 0 || req.RunID <= 0 || req.StepID <= 0 || req.Generation <= 0 || req.OwnerToken == "" || req.SourceFingerprint == "" {
		return StagedThumbnail{}, fmt.Errorf("thumbnail stage: invalid publication identity")
	}
	f, err := storage.OpenPlaintext(db, vault, req.MediaID, req.SourcePath)
	if err != nil {
		return StagedThumbnail{}, err
	}
	_, _, decodeErr := image.DecodeConfig(f)
	_ = f.Close()
	if decodeErr != nil {
		return StagedThumbnail{}, fmt.Errorf("%w: %v", ErrInvalidImage, decodeErr)
	}
	stageID := uuid.NewString()
	dir := filepath.Join(baseDir, fmt.Sprintf("generation-%d", req.Generation), stageID)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return StagedThumbnail{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dir)
		}
	}()
	renderVariant := func(name, kind string, edge int) (StagedVariant, error) {
		path := filepath.Join(dir, name)
		scale := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", edge, edge)
		if _, e := storage.RunFFmpeg(ctx, db, vault, ffmpegPath, req.MediaID, req.SourcePath, 0, 0, nil, []string{"-hide_banner", "-loglevel", "error", "-vf", scale, "-q:v", "4", path}, ""); e != nil {
			return StagedVariant{}, fmt.Errorf("ffmpeg thumb: %w", e)
		}
		if e := runCommitGuard(ctx); e != nil {
			return StagedVariant{}, e
		}
		size, hash, e := hashFile(path)
		if e != nil {
			return StagedVariant{}, e
		}
		v := StagedVariant{Kind: kind, LogicalName: name, Path: path, Hash: hash, Size: size}
		if derived != nil && storage.NeedsDerivedEncryption(derived.DB, req.MediaID) {
			v.Derived, e = derived.StagePath(ctx, req.MediaID, kind, name, path)
			if e != nil {
				return StagedVariant{}, e
			}
			_ = os.Remove(path)
			v.Path = v.Derived.EncPath()
			size, hash, e = hashFile(v.Path)
			v.Size, v.Hash = size, hash
		}
		return v, e
	}
	thumb, e := renderVariant("thumb.jpg", "photo_thumb", ThumbMaxEdge)
	if e != nil {
		return StagedThumbnail{}, e
	}
	medium, e := renderVariant("medium.jpg", "photo_medium", MediumMaxEdge)
	if e != nil {
		return StagedThumbnail{}, e
	}
	cleanup = false
	return StagedThumbnail{Stage: publication.StageRecord{StageID: stageID, Request: req, Kind: publication.ArtifactThumbnail, State: "staged", OriginalPath: req.SourcePath, StagedPath: dir}, Thumb: thumb, Medium: medium}, nil
}

func hashFile(path string) (int64, string, error) {
	f, e := os.Open(path)
	if e != nil {
		return 0, "", e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), e
}
