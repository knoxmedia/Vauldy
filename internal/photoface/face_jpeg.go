package photoface

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	"os"

	"knox-media/internal/storage"
)

func ValidateFaceJPEG(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return validateFaceJPEGReader(f)
}
func validateFaceJPEGReader(r interface{ Read([]byte) (int, error) }) error {
	cfg, format, err := image.DecodeConfig(r)
	if err != nil {
		return err
	}
	if format != "jpeg" || cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("invalid face jpeg")
	}
	return nil
}
func (w *Worker) validateEncryptedFaceArtifact(ctx context.Context, mediaID, faceID int64) error {
	enc, ok := storage.LookupEncPath(w.DB, mediaID, FaceThumbnailArtifactKind, FaceThumbnailLogicalName(faceID))
	if !ok {
		return os.ErrNotExist
	}
	seeker, err := storage.OpenDerivedArtifactSeeker(w.DB, w.Vault, mediaID, enc, FaceThumbnailArtifactKind, FaceThumbnailLogicalName(faceID))
	if err != nil {
		return err
	}
	defer seeker.Close()
	if err = ctx.Err(); err != nil {
		return err
	}
	return validateFaceJPEGReader(seeker)
}
