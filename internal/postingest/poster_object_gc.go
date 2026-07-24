package postingest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"knox-media/internal/store"
)

var posterObjectName = regexp.MustCompile(`^[0-9a-f]{64}\.jpg$`)
var withImmediatePosterObjectTx = store.WithImmediateConnTx
var posterObjectRename = os.Rename
var posterObjectDeleteQuarantine = os.Remove
var posterObjectBeforeImmediate = func() {}
var posterObjectCursors = struct {
	sync.Mutex
	next map[string]int
}{next: map[string]int{}}

func exactPosterObjectPath(uploadRoot, path string) bool {
	root := filepath.Join(strings.TrimSpace(uploadRoot), "posters", "objects", "sha256")
	absRoot, e := filepath.Abs(root)
	if e != nil {
		return false
	}
	absPath, e := filepath.Abs(path)
	if e != nil || !pathInsideResolvedRoot(absRoot, absPath) {
		return false
	}
	rel, e := filepath.Rel(absRoot, absPath)
	if e != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 2 || len(parts[0]) != 2 || !posterObjectName.MatchString(parts[1]) || parts[0] != parts[1][:2] {
		return false
	}
	for _, c := range parts[0] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	st, e := os.Lstat(absPath)
	return e == nil && st.Mode().IsRegular() && st.Mode()&os.ModeSymlink == 0
}
func prunePosterObjectPrefix(uploadRoot, path string) {
	root := filepath.Join(uploadRoot, "posters", "objects", "sha256")
	dir := filepath.Dir(path)
	if pathInsideResolvedRoot(root, dir) && !sameResolvedPath(root, dir) {
		_ = os.Remove(dir)
	}
}

func posterPathReferenceCountTx(ctx context.Context, tx store.SQLExecutor, path, url string) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM media_derived_assets WHERE enc_path=?)+(SELECT COUNT(*) FROM media_ingest_evidence WHERE json_extract(artifact_refs_json,'$.path')=? OR json_extract(artifact_refs_json,'$.url')=?)+(SELECT COUNT(*) FROM media_asset_stage_journal WHERE state!='committed' AND (json_extract(hashes_sizes_json,'$.path')=? OR json_extract(hashes_sizes_json,'$.url')=?))+(SELECT COUNT(*) FROM poster_repair_stage WHERE state!='committed' AND (json_extract(hashes_sizes_json,'$.path')=? OR json_extract(hashes_sizes_json,'$.url')=?))+(SELECT COUNT(*) FROM media WHERE json_extract(meta_json,'$.scrape.poster')=? OR json_extract(meta_json,'$.scrape.extra.poster')=?)`, path, path, url, path, url, path, url, url, url).Scan(&n)
	return n, err
}

// ReconcilePosterObjects scans at most limit exact CAS objects and stale seal temps.
func ReconcilePosterObjects(ctx context.Context, db *sql.DB, uploadRoot string, limit int, minAge time.Duration) (checked, cleaned int, retErr error) {
	if db == nil {
		return 0, 0, fmt.Errorf("poster object reconcile: database is required")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	root := filepath.Join(strings.TrimSpace(uploadRoot), "posters", "objects", "sha256")
	rootInfo, e := os.Lstat(root)
	if os.IsNotExist(e) {
		return 0, 0, nil
	}
	if e != nil {
		return 0, 0, e
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return 0, 0, fmt.Errorf("poster object root is unsafe")
	}
	entries, e := os.ReadDir(root)
	if e != nil {
		return 0, 0, e
	}
	var candidates []string
	quarantineDir := filepath.Join(root, "quarantine")
	if qs, x := os.ReadDir(quarantineDir); x == nil {
		for _, q := range qs {
			if !q.IsDir() && q.Type()&os.ModeSymlink == 0 {
				candidates = append(candidates, filepath.Join(quarantineDir, q.Name()))
			}
		}
	}
	for _, prefix := range entries {
		if prefix.Type()&os.ModeSymlink != 0 || !prefix.IsDir() || len(prefix.Name()) != 2 {
			continue
		}
		dir := filepath.Join(root, prefix.Name())
		children, x := os.ReadDir(dir)
		if x != nil {
			return checked, cleaned, x
		}
		for _, child := range children {
			if child.Type()&os.ModeSymlink != 0 {
				continue
			}
			if child.IsDir() {
				continue
			}
			candidates = append(candidates, filepath.Join(dir, child.Name()))
		}
	}
	sort.Strings(candidates)
	posterObjectCursors.Lock()
	start := posterObjectCursors.next[root]
	if start >= len(candidates) {
		start = 0
	}
	posterObjectCursors.Unlock()
	if start > 0 {
		candidates = append(candidates[start:], candidates[:start]...)
	}
	cutoff := time.Now().Add(-minAge)
	visited := 0
	for _, p := range candidates {
		visited++
		info, x := os.Lstat(p)
		if x != nil {
			continue
		}
		name := filepath.Base(p)
		isQuarantine := strings.HasSuffix(name, ".quarantine") && strings.Contains(filepath.ToSlash(p), "/sha256/quarantine/")
		isTemp := (strings.HasPrefix(name, ".seal-") && strings.HasSuffix(name, ".tmp")) || strings.HasPrefix(name, ".tmp-") || isQuarantine
		isObject := exactPosterObjectPath(uploadRoot, p)
		if (!isTemp && !isObject) || !info.ModTime().Before(cutoff) {
			continue
		}
		if checked >= limit {
			break
		}
		checked++
		if isTemp {
			if info.ModTime().Before(cutoff) {
				if x = os.Remove(p); x == nil {
					cleaned++
					prunePosterObjectPrefix(uploadRoot, p)
				}
			}
			continue
		}
		hash := strings.TrimSuffix(name, ".jpg")
		url := "/uploads/posters/objects/sha256/" + hash[:2] + "/" + name
		posterObjectBeforeImmediate()
		quarantineRoot := filepath.Join(root, "quarantine")
		quarantine := filepath.Join(quarantineRoot, hash+"."+uuid.NewString()+".quarantine")
		renamed := false
		outcome, x := withImmediatePosterObjectTx(ctx, db, func(tx store.ImmediateConnTx) error {
			info, e := os.Lstat(p)
			if e != nil {
				return e
			}
			if !exactPosterObjectPath(uploadRoot, p) || !info.ModTime().Before(cutoff) {
				return nil
			}
			refs, e := posterPathReferenceCountTx(ctx, tx, p, url)
			if e != nil {
				return e
			}
			if refs != 0 {
				return nil
			}
			if e = os.MkdirAll(quarantineRoot, 0700); e != nil {
				return e
			}
			if e = posterObjectRename(p, quarantine); e != nil {
				return e
			}
			renamed = true
			return nil
		})
		if x != nil {
			if renamed && outcome.CommitAttempted && !outcome.CommitConfirmed {
				return checked, cleaned, x
			}
			if !os.IsNotExist(x) {
				retErr = x
			}
			continue
		}
		if !renamed {
			continue
		}
		if x = posterObjectDeleteQuarantine(quarantine); x != nil && !os.IsNotExist(x) {
			retErr = x
			continue
		}
		cleaned++
		prunePosterObjectPrefix(uploadRoot, p)
	}
	if len(candidates) > 0 {
		posterObjectCursors.Lock()
		posterObjectCursors.next[root] = (start + visited) % len(candidates)
		posterObjectCursors.Unlock()
	}
	return checked, cleaned, retErr
}
