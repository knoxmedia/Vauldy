package publication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"knox-media/internal/coreiface"
	"knox-media/internal/store"
)

// PreflightResources are executable startup proofs supplied by server assembly.
type PreflightResources struct {
	StageRoot, QuarantineRoot, DerivedRoot                  string
	VaultReady, KeyReady, PosterResolver, ThumbnailResolver bool
	ResolveStage                                            func(context.Context, int64, string) (string, error)
}

var publicationV2Tables = []string{"media_ingest_run", "media_ingest_step", "media_ingest_step_dependency", "media_ingest_evidence", "media_asset_stage_journal", "post_ingest_task", "scrape_task", "transcode_task"}
var publicationV2Indexes = []string{"idx_ingest_dependency_visible", "idx_asset_stage_recovery", "idx_post_ingest_claim", "idx_scrape_task_claim"}

// PreflightPublicationV2 validates the persisted graph and executable capabilities before claims begin.
func PreflightPublicationV2(ctx context.Context, db *sql.DB, planner *Planner, registry coreiface.CapabilityRegistry, resources PreflightResources) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if db == nil || planner == nil || registry == nil {
		return nil, errors.New("publication v2 preflight: database, planner, and capability registry are required")
	}
	if err := store.ValidatePublicationV2Schema(ctx, db); err != nil {
		return nil, fmt.Errorf("publication v2 preflight: schema: %w", err)
	}

	for _, table := range publicationV2Tables {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			return nil, fmt.Errorf("publication v2 preflight: required table %s unavailable", table)
		}
	}
	for _, required := range []StepType{StepPoster, StepThumbnail} {
		if !registry.Available(string(required)) {
			return nil, fmt.Errorf("publication v2 preflight: required adapter %s unavailable", required)
		}
	}
	for _, index := range publicationV2Indexes {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&n); err != nil || n != 1 {
			return nil, fmt.Errorf("publication v2 preflight: required index %s unavailable", index)
		}
	}
	var encrypted int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM library WHERE COALESCE(encrypted_assets_enabled,0)=1`).Scan(&encrypted); err != nil {
		return nil, fmt.Errorf("publication v2 preflight: inspect encrypted libraries: %w", err)
	}
	if encrypted > 0 {
		gaps := make([]string, 0, 8)
		if !planner.options.EncryptGlobal {
			gaps = append(gaps, "global encryption configuration")
		}
		if !registry.Available(string(StepEncrypt)) {
			gaps = append(gaps, "encrypt capability")
		}
		if !resources.VaultReady {
			gaps = append(gaps, "vault")
		}
		if !resources.KeyReady {
			gaps = append(gaps, "key")
		}
		if !resources.PosterResolver {
			gaps = append(gaps, "poster resolver")
		}
		if !resources.ThumbnailResolver {
			gaps = append(gaps, "thumbnail resolver")
		}
		for label, root := range map[string]string{"stage root": resources.StageRoot, "quarantine root": resources.QuarantineRoot, "derived root": resources.DerivedRoot} {
			if err := probeSecureWritableRoot(root); err != nil {
				gaps = append(gaps, label+": "+err.Error())
			}
		}
		if len(gaps) > 0 {
			sort.Strings(gaps)
			return nil, fmt.Errorf("publication v2 preflight: encrypted library capability gap: %s", strings.Join(gaps, "; "))
		}
	}
	warnings := []string{}
	var preview, prepare int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM library WHERE COALESCE(preview_extract,0)=1`).Scan(&preview)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM library WHERE COALESCE(jit_prepare_on_ingest,0)=1`).Scan(&prepare)
	optional := []struct {
		name    string
		enabled bool
	}{{"scrape", true}, {"preview", preview > 0}, {"subtitle", planner.options.SubtitleAuto}, {"prepare", prepare > 0 && planner.options.PreparePlanner != nil}}
	for _, item := range optional {
		if item.enabled && !registry.Available(item.name) {
			warnings = append(warnings, fmt.Sprintf("adapter_unavailable:%s", item.name))
		}
	}
	return warnings, nil
}

func probeSecureWritableRoot(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return errors.New("not configured")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".publication-v2-preflight-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write([]byte{0})
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func cleanRoot(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}
