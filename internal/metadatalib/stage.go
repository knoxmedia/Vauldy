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

type ScrapeStageClaim struct {
	TaskID, MediaID, RunID, StepID, Generation int64
	LeaseOwner                                 string
	Attempt, RetryRound                        int
}

var ErrScrapeStageClaimLost = fmt.Errorf("scrape artwork stage claim lost")

const scrapeStageClaimExistsSQL = `EXISTS(SELECT 1 FROM scrape_task q JOIN media_ingest_step st ON st.id=q.ingest_step_id JOIN media_ingest_run r ON r.id=q.ingest_run_id JOIN media m ON m.id=q.media_id WHERE q.id=? AND q.media_id=? AND q.ingest_run_id=? AND q.ingest_step_id=? AND q.generation=? AND q.status='running' AND q.lease_owner=? AND q.retry_round=? AND q.lease_until>CURRENT_TIMESTAMP AND st.id=? AND st.run_id=? AND st.media_id=? AND st.generation=? AND st.status='running' AND st.lease_owner=? AND st.attempts=? AND st.lease_until>CURRENT_TIMESTAMP AND r.id=? AND r.media_id=? AND r.generation=? AND r.status IN ('processing','published','degraded') AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=?)`

func scrapeStageClaimArgs(c ScrapeStageClaim) []any {
	return []any{c.TaskID, c.MediaID, c.RunID, c.StepID, c.Generation, c.LeaseOwner, c.RetryRound, c.StepID, c.RunID, c.MediaID, c.Generation, c.LeaseOwner, c.Attempt, c.RunID, c.MediaID, c.Generation, c.Generation}
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

func StageScrapeImagesDurable(ctx context.Context, db *sql.DB, root, upload string, claim ScrapeStageClaim, stageID string, res *scraper.ScrapeResult) (StagedScrapeArtwork, error) {
	if db == nil {
		return StagedScrapeArtwork{}, fmt.Errorf("scrape artwork stage database required")
	}
	dir := filepath.Join(MediaDir(root, claim.MediaID), "stages", fmt.Sprintf("g%d", claim.Generation), stageID)
	fp := "scrape_artwork:" + stageID
	var reserved bool
	_, e := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		args := []any{stageID, claim.MediaID, claim.RunID, claim.StepID, claim.Generation, claim.LeaseOwner, fp, dir, claim.TaskID, claim.Attempt, claim.RetryRound}
		args = append(args, scrapeStageClaimArgs(claim)...)
		r, err := tx.ExecContext(ctx, `INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json,scrape_task_id,scrape_attempt,scrape_retry_round) SELECT ?,?,?,?,?,?,?,'scrape_artwork','staged',?,'{}',?,?,? WHERE `+scrapeStageClaimExistsSQL+` ON CONFLICT(stage_id) DO NOTHING`, args...)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		reserved = n == 1
		return err
	})
	if e != nil {
		return StagedScrapeArtwork{}, e
	}
	if !reserved {
		return StagedScrapeArtwork{}, ErrScrapeStageClaimLost
	}
	out, e := StageScrapeImages(ctx, root, upload, claim.MediaID, claim.Generation, stageID, res)
	if e != nil {
		_ = os.RemoveAll(dir)
		args := []any{stageID, claim.TaskID, claim.Attempt, claim.RetryRound}
		args = append(args, scrapeStageClaimArgs(claim)...)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM media_asset_stage_journal WHERE stage_id=? AND scrape_task_id=? AND scrape_attempt=? AND scrape_retry_round=? AND `+scrapeStageClaimExistsSQL, args...)
		return out, e
	}
	raw, _ := json.Marshal(out.Images)
	args := []any{string(raw), stageID, claim.TaskID, claim.Attempt, claim.RetryRound}
	args = append(args, scrapeStageClaimArgs(claim)...)
	r, e := db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET hashes_sizes_json=?,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='staged' AND scrape_task_id=? AND scrape_attempt=? AND scrape_retry_round=? AND `+scrapeStageClaimExistsSQL, args...)
	if e != nil {
		return out, e
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return out, ErrScrapeStageClaimLost
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
	active := `j.scrape_task_id IS NOT NULL AND j.scrape_attempt IS NOT NULL AND j.scrape_retry_round IS NOT NULL AND EXISTS(SELECT 1 FROM scrape_task q JOIN media m ON m.id=q.media_id JOIN media_ingest_step st ON st.id=q.ingest_step_id JOIN media_ingest_run r ON r.id=q.ingest_run_id WHERE q.id=j.scrape_task_id AND q.media_id=j.media_id AND q.ingest_run_id=j.run_id AND q.ingest_step_id=j.step_id AND q.generation=j.generation AND q.lease_owner=j.owner_token AND q.retry_round=j.scrape_retry_round AND q.status='running' AND q.lease_until>CURRENT_TIMESTAMP AND st.id=j.step_id AND st.run_id=j.run_id AND st.media_id=j.media_id AND st.generation=j.generation AND st.status='running' AND st.lease_owner=j.owner_token AND st.attempts=j.scrape_attempt AND st.lease_until>CURRENT_TIMESTAMP AND r.id=j.run_id AND r.media_id=j.media_id AND r.generation=j.generation AND r.status IN ('processing','published','degraded') AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=j.generation)`
	base := `artifact_kind='scrape_artwork' AND state IN ('staged','quarantined') AND updated_at < datetime(CURRENT_TIMESTAMP,'-10 minutes') AND NOT EXISTS(SELECT 1 FROM media_ingest_evidence e WHERE e.stage_id=j.stage_id) AND recovery_error NOT IN ('cleaned_unreferenced','unsafe_path') AND recovery_error NOT LIKE 'failed_closed:%'`
	rows, err := db.QueryContext(ctx, `SELECT stage_id,staged_path,state FROM media_asset_stage_journal j WHERE `+base+` AND (state='quarantined' OR NOT (`+active+`)) ORDER BY updated_at,stage_id LIMIT ?`, limit)
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
			if _, err := db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET state='failed_closed',recovery_error='failed_closed:unsafe_path',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND artifact_kind='scrape_artwork'`, c.id); err != nil {
				return n, err
			}
			continue
		}
		if c.state == "staged" {
			var changed bool
			_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
				r, e := tx.ExecContext(ctx, `UPDATE media_asset_stage_journal AS j SET state='quarantined',quarantine_path=staged_path,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND `+base+` AND NOT (`+active+`)`, c.id)
				if e != nil {
					return e
				}
				x, e := r.RowsAffected()
				changed = x == 1
				return e
			})
			if err != nil {
				return n, err
			}
			if !changed {
				continue
			}
		}
		if err := os.RemoveAll(c.path); err != nil {
			return n, err
		}
		r, err := db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET recovery_error='cleaned_unreferenced',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='quarantined'`, c.id)
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
