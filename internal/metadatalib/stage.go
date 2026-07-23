package metadatalib

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"knox-media/internal/scraper"
	"os"
	"path/filepath"
)

type StagedScrapeImage struct {
	Kind, Path, PublicURL, Hash string
	Size                        int64
}
type StagedScrapeArtwork struct {
	StageID             string
	MediaID, Generation int64
	Images              []StagedScrapeImage
}

func StageScrapeImages(ctx context.Context, root, uploadRoot string, mediaID, generation int64, stageID string, res *scraper.ScrapeResult) (StagedScrapeArtwork, error) {
	out := StagedScrapeArtwork{StageID: stageID, MediaID: mediaID, Generation: generation}
	if res == nil || root == "" || mediaID <= 0 {
		return out, nil
	}
	dir := filepath.Join(MediaDir(root, mediaID), "stages", fmt.Sprintf("g%d", generation), stageID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return out, err
	}
	for kind, raw := range collectRemoteImages(res) {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		ext := extFromURL(raw, kind)
		dest := filepath.Join(dir, kind+ext)
		var err error
		if src, ok := resolveUploadsFile(uploadRoot, raw); ok {
			err = copyFile(src, dest)
		} else {
			err = downloadToFile(raw, dest)
		}
		if err != nil {
			return out, err
		}
		b, e := os.ReadFile(dest)
		if e != nil {
			return out, e
		}
		sum := sha256.Sum256(b)
		pub := PublicURL(mediaID, filepath.ToSlash(filepath.Join("stages", fmt.Sprintf("g%d", generation), stageID, kind+ext)))
		out.Images = append(out.Images, StagedScrapeImage{Kind: kind, Path: dest, PublicURL: pub, Hash: hex.EncodeToString(sum[:]), Size: int64(len(b))})
	}
	return out, nil
}
func SelectStagedScrapeArtwork(res *scraper.ScrapeResult, stage StagedScrapeArtwork) {
	if res == nil {
		return
	}
	for _, i := range stage.Images {
		applyImageURL(res, i.Kind, i.PublicURL)
	}
}
