package metadatalib

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"knox-media/internal/scraper"
	"knox-media/internal/store"
	"os"
	"path/filepath"
	"strings"
)

type StagedScrapeImage struct {
	Kind, Path, PublicURL, Hash string
	Size                        int64
}
type StagedScrapeArtwork struct {
	Root                string
	StageID             string
	MediaID, Generation int64
	Images              []StagedScrapeImage
}

func StageScrapeImages(ctx context.Context, root, uploadRoot string, mediaID, generation int64, stageID string, res *scraper.ScrapeResult) (StagedScrapeArtwork, error) {
	out := StagedScrapeArtwork{StageID: stageID, MediaID: mediaID, Generation: generation, Root: root}
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

func StageScrapeImagesDurable(ctx context.Context, db *sql.DB, root, upload string, runID, stepID, mediaID, generation int64, owner, stageID string, res *scraper.ScrapeResult) (StagedScrapeArtwork, error) {
	if db == nil {
		return StagedScrapeArtwork{}, fmt.Errorf("scrape artwork stage database required")
	}
	dir := filepath.Join(MediaDir(root, mediaID), "stages", fmt.Sprintf("g%d", generation), stageID)
	fp := "scrape_artwork:" + stageID
	if _, e := db.ExecContext(ctx, `INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'scrape_artwork','staged',?,'{}') ON CONFLICT(stage_id) DO NOTHING`, stageID, mediaID, runID, stepID, generation, owner, fp, dir); e != nil {
		return StagedScrapeArtwork{}, e
	}
	out, e := StageScrapeImages(ctx, root, upload, mediaID, generation, stageID, res)
	if e != nil {
		_ = os.RemoveAll(dir)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM media_asset_stage_journal WHERE stage_id=? AND state='staged'`, stageID)
		return out, e
	}
	raw, _ := json.Marshal(out.Images)
	_, e = db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET hashes_sizes_json=?,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='staged' AND owner_token=?`, string(raw), stageID, owner)
	return out, e
}
func VerifyStagedScrapeArtwork(stage StagedScrapeArtwork) error {
	for _, v := range stage.Images {
		if !strings.HasPrefix(filepath.Clean(v.Path), filepath.Clean(stage.Root)+string(filepath.Separator)) {
			return fmt.Errorf("stage path outside trusted root")
		}
		b, e := os.ReadFile(v.Path)
		if e != nil {
			return e
		}
		sum := sha256.Sum256(b)
		if int64(len(b)) != v.Size || hex.EncodeToString(sum[:]) != v.Hash {
			return fmt.Errorf("staged scrape artwork hash mismatch: %s", v.Kind)
		}
	}
	return nil
}
func ReconcileScrapeArtworkStages(ctx context.Context, db *sql.DB, root string, limit int) (int, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, e := db.QueryContext(ctx, `SELECT stage_id,staged_path FROM media_asset_stage_journal j WHERE artifact_kind='scrape_artwork' AND state IN ('staged','quarantined') AND NOT EXISTS(SELECT 1 FROM media_ingest_evidence e WHERE e.stage_id=j.stage_id) LIMIT ?`, limit)
	if e != nil {
		return 0, e
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, path string
		if rows.Scan(&id, &path) != nil {
			continue
		}
		if strings.HasPrefix(filepath.Clean(path), filepath.Clean(root)+string(filepath.Separator)) {
			_ = os.RemoveAll(path)
			_, _ = db.ExecContext(ctx, `DELETE FROM media_asset_stage_journal WHERE stage_id=? AND state!='committed'`, id)
			n++
		}
	}
	return n, rows.Err()
}

var _ store.SQLExecutor
