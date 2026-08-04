package publication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"knox-media/internal/coreiface"
	"knox-media/internal/store"
)

type EncryptedLibrary struct {
	ID         int64
	Path, Mode string
}

type EncryptionPolicyValidator interface {
	ValidateEncryptedLibrary(context.Context, store.SQLExecutor, EncryptedLibrary) error
	ProbePosterResolver(context.Context) error
	ProbeThumbnailResolver(context.Context) error
}

type encryptedLibraryRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

func collectEncryptedLibraries(rows encryptedLibraryRows) (libs []EncryptedLibrary, retErr error) {
	if rows == nil {
		return nil, errors.New("nil encrypted library rows")
	}
	defer func() { retErr = errors.Join(retErr, rows.Close()) }()
	for rows.Next() {
		var lib EncryptedLibrary
		if err := rows.Scan(&lib.ID, &lib.Path, &lib.Mode); err != nil {
			return nil, err
		}
		libs = append(libs, lib)
	}
	return libs, rows.Err()
}

// PreflightPublicationV2 validates exact schema and executable capabilities before claims begin.
func PreflightPublicationV2(ctx context.Context, db *sql.DB, planner *Planner, registry coreiface.CapabilityRegistry, resources EncryptionPolicyValidator) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if db == nil || planner == nil || registry == nil || resources == nil {
		return nil, errors.New("publication v2 preflight: database, planner, registry, and resource validator are required")
	}
	if err := store.ValidatePublicationV2Schema(ctx, db); err != nil {
		return nil, fmt.Errorf("publication v2 preflight: schema: %w", err)
	}
	for _, required := range []StepType{StepPoster, StepThumbnail} {
		if !registry.Available(string(required)) {
			return nil, fmt.Errorf("publication v2 preflight: required adapter %s unavailable", required)
		}
	}
	if err := resources.ProbePosterResolver(ctx); err != nil {
		return nil, fmt.Errorf("publication v2 preflight: poster resolver: %w", err)
	}
	if err := resources.ProbeThumbnailResolver(ctx); err != nil {
		return nil, fmt.Errorf("publication v2 preflight: thumbnail resolver: %w", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT id,COALESCE(path,''),COALESCE(encrypted_assets_dir_mode,'library') FROM library WHERE COALESCE(encrypted_assets_enabled,0)=1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	libs, err := collectEncryptedLibraries(rows)
	if err != nil {
		return nil, fmt.Errorf("publication v2 preflight: enumerate encrypted libraries: %w", err)
	}
	if len(libs) > 0 && (!planner.options.EncryptGlobal || !registry.Available(string(StepEncrypt))) {
		return nil, errors.New("publication v2 preflight: encrypted library requires executable encrypt capability")
	}
	warnings := []string{}
	for _, lib := range libs {
		if err = resources.ValidateEncryptedLibrary(ctx, db, lib); err != nil {
			warnings = append(warnings, fmt.Sprintf("encrypted_library_unavailable:%d:%v", lib.ID, err))
		}
	}
	var preview, prepare int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM library WHERE COALESCE(preview_extract,0)=1`).Scan(&preview)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM library WHERE COALESCE(jit_prepare_on_ingest,0)=1`).Scan(&prepare)
	for _, item := range []struct {
		name    string
		enabled bool
	}{{"scrape", true}, {"preview", preview > 0}, {"subtitle", planner.options.SubtitleAuto}, {"prepare", prepare > 0 && planner.options.PreparePlanner != nil}} {
		if item.enabled && !registry.Available(item.name) {
			warnings = append(warnings, "adapter_unavailable:"+item.name)
		}
	}
	sort.Strings(warnings)
	return warnings, nil
}

func canonicalPrivateRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("root not configured")
	}
	return root, nil
}
