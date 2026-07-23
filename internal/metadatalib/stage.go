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
	"time"
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
	r, e := db.ExecContext(ctx, `INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'scrape_artwork','staged',?,'{}') ON CONFLICT(stage_id) DO NOTHING`, stageID, mediaID, runID, stepID, generation, owner, fp, dir)
	if e != nil {
		return StagedScrapeArtwork{}, e
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return StagedScrapeArtwork{}, fmt.Errorf("scrape artwork stage unavailable")
	}
	out, e := StageScrapeImages(ctx, root, upload, mediaID, generation, stageID, res)
	if e != nil {
		_ = os.RemoveAll(dir)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM media_asset_stage_journal WHERE stage_id=? AND state='staged'`, stageID)
		return out, e
	}
	raw, _ := json.Marshal(out.Images)
	r, e = db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET hashes_sizes_json=?,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='staged' AND owner_token=?`, string(raw), stageID, owner)
	if e != nil {
		return out, e
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return out, fmt.Errorf("scrape artwork stage ownership lost")
	}
	return out, nil
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
	rows, err := db.QueryContext(ctx, `SELECT stage_id,staged_path,state FROM media_asset_stage_journal j WHERE artifact_kind='scrape_artwork' AND state IN ('staged','quarantined') AND updated_at < datetime(CURRENT_TIMESTAMP,'-10 minutes') AND NOT EXISTS(SELECT 1 FROM media_ingest_evidence e WHERE e.stage_id=j.stage_id) AND (state='quarantined' OR NOT EXISTS(SELECT 1 FROM scrape_task q JOIN media m ON m.id=q.media_id JOIN media_ingest_step s ON s.id=q.ingest_step_id AND s.run_id=q.ingest_run_id AND s.media_id=q.media_id AND s.generation=q.generation WHERE q.media_id=j.media_id AND q.ingest_run_id=j.run_id AND q.ingest_step_id=j.step_id AND q.generation=j.generation AND q.lease_owner=j.owner_token AND q.status='running' AND q.lease_until>CURRENT_TIMESTAMP AND s.status='running' AND s.lease_owner=j.owner_token AND m.ingest_generation=j.generation)) LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct{ id, path, state string }
	var all []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.path, &c.state); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	n := 0
	for _, c := range all {
		if !strings.HasPrefix(filepath.Clean(c.path), filepath.Clean(root)+string(filepath.Separator)) {
			continue
		}
		if c.state == "staged" {
			r, err := db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET state='quarantined',quarantine_path=staged_path,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='staged' AND updated_at < datetime(CURRENT_TIMESTAMP,'-10 minutes') AND NOT EXISTS(SELECT 1 FROM media_ingest_evidence WHERE stage_id=?)`, c.id, c.id)
			if err != nil {
				return n, err
			}
			if x, _ := r.RowsAffected(); x != 1 {
				continue
			}
		}
		if err := os.RemoveAll(c.path); err != nil {
			return n, err
		}
		r, err := db.ExecContext(ctx, `DELETE FROM media_asset_stage_journal WHERE stage_id=? AND state='quarantined'`, c.id)
		if err != nil {
			return n, err
		}
		if x, _ := r.RowsAffected(); x == 1 {
			n++
		}
	}
	return n, nil
}

var _ store.SQLExecutor

func RunScrapeArtworkStageReconciler(ctx context.Context, db *sql.DB, root string, interval time.Duration, limit int, report func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		_, e := ReconcileScrapeArtworkStages(ctx, db, root, limit)
		if e != nil && report != nil {
			report(e)
		}
	}
	run()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
