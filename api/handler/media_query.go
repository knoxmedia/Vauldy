package handler

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/photoclass"
)

type mediaSort string

const optimizationAssetRecordedSQL = `CASE
 WHEN lower(COALESCE(m.file_type,''))!='video' OR NULLIF(TRIM(m.file_path),'') IS NULL THEN 0
 WHEN lower(TRIM(m.file_path)) NOT LIKE '%.enc' THEN 1
 WHEN COALESCE(l.encrypted_assets_cleanup_plaintext,0)=0
  AND mea.status='encrypted'
  AND NULLIF(TRIM(mea.plain_path),'') IS NOT NULL THEN 1
 ELSE 0 END`

const (
	mediaSortIDDesc      mediaSort = "id_desc"
	mediaSortCreatedDesc mediaSort = "created_desc"
	mediaSortTakenDesc   mediaSort = "taken_desc"
)

type mediaCursor struct {
	SortKey *string
	ID      int64
}
type mediaListSpec struct {
	LibraryID                                                             *int64
	FileType, Search, PhotoTag, PhotoPlace, PhotoPerson, PublicationState string
	AllowedLibraryIDs                                                     []int64
	FolderScope                                                           map[int64][]string
	RestrictLibraries                                                     bool
	Sort                                                                  mediaSort
	Limit, BatchSize                                                      int
	Cursor                                                                *mediaCursor
	UserID                                                                int64
	IncludeUnpublished                                                    bool
}
type mediaQuery struct {
	SQL           string
	Args          []any
	NeedsGoFilter bool
	queryer       mediaBatchQueryer
}
type mediaListStats struct{ Batches, Candidates, Rejected, Returned int }
type mediaBatchQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type mediaListRow struct {
	ID                                                                                                                             int64
	LibraryID                                                                                                                      sql.NullInt64
	FileID, Title, OriginalTitle, FilePath, FileType, Format, Status, CreatedAt                                                    sql.NullString
	LastPlayAt, ReleaseDate, PosterURL, BackdropURL, PhotoTakenAt, PhotoTagsRaw                                                    sql.NullString
	MusicAlbumTitle, MusicArtist, SortKey                                                                                          sql.NullString
	Duration, Width, Height, Bitrate, ReleaseYear, Scraped, EncryptedAsset, OptimizationAssetRecorded, MusicAlbumID, PlayCompleted sql.NullInt64
	PhotoTags, PhotoTagIDs                                                                                                         []string
	PublicationState, PublishedAt, PublicationError, IngestRunStatus                                                               sql.NullString
	IngestGeneration                                                                                                               sql.NullInt64
}

func parseMediaListSpec(c *gin.Context, profile userPermissionProfile, userID int64) (mediaListSpec, error) {
	spec := mediaListSpec{
		FileType: strings.TrimSpace(c.Query("file_type")), Search: strings.TrimSpace(c.Query("q")),
		PhotoTag: strings.TrimSpace(c.Query("photo_tag")), PhotoPlace: strings.TrimSpace(c.Query("photo_place")),
		PhotoPerson: strings.TrimSpace(c.Query("photo_person")), UserID: userID,
		FolderScope: profile.AllowedLibraryFolders, Sort: mediaSort(c.DefaultQuery("sort", string(mediaSortIDDesc))),
	}
	switch spec.Sort {
	case mediaSortIDDesc, mediaSortCreatedDesc, mediaSortTakenDesc:
	default:
		return spec, fmt.Errorf("invalid sort")
	}
	if raw := strings.TrimSpace(c.Query("cursor")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 || spec.Sort != mediaSortIDDesc {
			return spec, fmt.Errorf("invalid cursor")
		}
		spec.Cursor = &mediaCursor{ID: id}
	}
	if raw := strings.TrimSpace(c.Query("library_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return spec, fmt.Errorf("invalid library_id")
		}
		spec.LibraryID = &id
	}
	if strings.EqualFold(profile.LibraryScope, "selected") {
		spec.RestrictLibraries = true
		spec.AllowedLibraryIDs = make([]int64, 0, len(profile.AllowedLibraryIDs))
		for id := range profile.AllowedLibraryIDs {
			spec.AllowedLibraryIDs = append(spec.AllowedLibraryIDs, id)
		}
		sort.Slice(spec.AllowedLibraryIDs, func(i, j int) bool { return spec.AllowedLibraryIDs[i] < spec.AllowedLibraryIDs[j] })
	}
	maxLimit := 500
	if spec.FileType == "image" {
		maxLimit = 5000
	}
	spec.Limit = maxLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= maxLimit {
			spec.Limit = n
		}
	}
	spec.BatchSize = spec.Limit * 2
	if spec.BatchSize < 100 {
		spec.BatchSize = 100
	}
	if spec.BatchSize > 500 {
		spec.BatchSize = 500
	}
	return spec, nil
}

// ListMedia receives databases opened by store.OpenSQLiteContext. Startup synchronously
// completes the media-sort invariant and indexes before the DB can reach HTTP; the
// COALESCE fallback remains for defensive compatibility with direct/raw test fixtures.
func sortSQL(s mediaSort) (keyExpr, orderBy string, err error) {
	switch s {
	case mediaSortIDDesc:
		return "CAST(m.id AS TEXT)", "m.id DESC", nil
	case mediaSortCreatedDesc:
		return "m.created_at_sort", "m.created_at_sort DESC, m.id DESC", nil
	case mediaSortTakenDesc:
		return "COALESCE(m.photo_taken_at,m.created_at_sort)", "COALESCE(m.photo_taken_at,m.created_at_sort) DESC, m.id DESC", nil
	default:
		return "", "", fmt.Errorf("invalid sort")
	}
}

func buildMediaQuery(spec mediaListSpec, cursor *mediaCursor, batchLimit int) (mediaQuery, error) {
	keyExpr, orderBy, err := sortSQL(spec.Sort)
	if err != nil {
		return mediaQuery{}, err
	}
	if batchLimit <= 0 {
		return mediaQuery{}, fmt.Errorf("invalid batch limit")
	}

	q := `WITH params AS (SELECT ? AS user_id),
candidates AS MATERIALIZED (SELECT m.* FROM media m WHERE 1=1`
	args := []any{spec.UserID}
	if !spec.IncludeUnpublished {
		q += ` AND ` + mediaPublicationVisiblePredicate("m")
	}
	if spec.PublicationState != "" {
		q += ` AND m.publication_state=?`
		args = append(args, spec.PublicationState)
	}
	if spec.LibraryID != nil {
		q += ` AND m.library_id=?`
		args = append(args, *spec.LibraryID)
	}
	if spec.RestrictLibraries || len(spec.AllowedLibraryIDs) > 0 {
		if len(spec.AllowedLibraryIDs) == 0 {
			q += ` AND 0=1`
		} else {
			q += ` AND m.library_id IN (` + strings.TrimRight(strings.Repeat("?,", len(spec.AllowedLibraryIDs)), ",") + ")"
			for _, id := range spec.AllowedLibraryIDs {
				args = append(args, id)
			}
		}
	}
	if spec.FileType != "" {
		q += ` AND m.file_type=?`
		args = append(args, spec.FileType)
	}
	if spec.PhotoPlace != "" {
		q += ` AND m.photo_place_id=?`
		args = append(args, spec.PhotoPlace)
	}
	if spec.PhotoPerson != "" {
		q += ` AND EXISTS(SELECT 1 FROM photo_face pf WHERE pf.media_id=m.id AND pf.person_id=?)`
		args = append(args, spec.PhotoPerson)
	}
	if spec.Search != "" {
		q, args = appendMediaTextSearchFilter(q, args, spec.Search)
	}
	if cursor != nil {
		if spec.Sort == mediaSortIDDesc {
			q += ` AND m.id < ?`
			args = append(args, cursor.ID)
		} else if cursor.SortKey == nil {
			q += ` AND (` + keyExpr + ` IS NULL AND m.id < ?)`
			args = append(args, cursor.ID)
			orderBy = "m.id DESC"
		} else {
			q += ` AND ((` + keyExpr + ` < ?) OR (` + keyExpr + ` = ? AND m.id < ?) OR ` + keyExpr + ` IS NULL)`
			args = append(args, *cursor.SortKey, *cursor.SortKey, cursor.ID)
		}
	}
	q += ` ORDER BY ` + orderBy + ` LIMIT ?)`
	args = append(args, batchLimit)
	q += `,
pmax AS (
 SELECT pp.file_id,MAX(pp.update_at) AS last_play_at FROM play_progress pp JOIN candidates c ON c.file_id=pp.file_id GROUP BY pp.file_id
),
pu AS (
 SELECT pp.file_id,MAX(COALESCE(pp.completed,0)) AS completed
 FROM play_progress pp JOIN candidates c ON c.file_id=pp.file_id
 WHERE pp.user_id=(SELECT user_id FROM params) GROUP BY pp.file_id
),
mt_pick AS (
 SELECT mt.media_id,MIN(mt.id) AS track_id FROM music_track mt JOIN candidates c ON c.id=mt.media_id GROUP BY mt.media_id
)
SELECT m.id,m.library_id,m.file_id,m.title,m.original_title,m.file_path,m.file_type,m.duration,m.width,m.height,m.bitrate,m.format,m.status,m.created_at,
pmax.last_play_at,COALESCE(pu.completed,0),
COALESCE(NULLIF(json_extract(m.meta_json,'$.scrape.release_date'),''),NULLIF(json_extract(m.meta_json,'$.release_date'),'')),
COALESCE(CAST(NULLIF(json_extract(m.meta_json,'$.scrape.year'),'') AS INTEGER),CAST(NULLIF(json_extract(m.meta_json,'$.year'),'') AS INTEGER),CAST(substr(COALESCE(NULLIF(json_extract(m.meta_json,'$.scrape.release_date'),''),NULLIF(json_extract(m.meta_json,'$.release_date'),'')),1,4) AS INTEGER),0),
COALESCE(NULLIF(TRIM(json_extract(m.meta_json,'$.scrape.poster')),''),NULLIF(TRIM(json_extract(m.meta_json,'$.scrape.extra.poster')),'')),
COALESCE(NULLIF(TRIM(json_extract(m.meta_json,'$.scrape.backdrop')),''),NULLIF(TRIM(json_extract(m.meta_json,'$.scrape.extra.backdrop')),''),NULLIF(TRIM(json_extract(m.meta_json,'$.scrape.extra.series_backdrop')),'')),
NULLIF(json_extract(m.meta_json,'$.photo.taken_at'),''),COALESCE(json_extract(m.meta_json,'$.photo.tags'),'[]'),
mt.album_id,COALESCE(NULLIF(TRIM(ma.title),''),''),COALESCE(NULLIF(TRIM(mt.artist_display),''),NULLIF(TRIM(ar.name),''),''),
CASE WHEN COALESCE(json_extract(m.meta_json,'$.scrape.source'),'') NOT IN ('','aggregated-stub') AND COALESCE(json_extract(m.meta_json,'$.scrape.extra.note'),'')!='stub' AND (NULLIF(TRIM(json_extract(m.meta_json,'$.scrape.overview')),'') IS NOT NULL OR NULLIF(TRIM(json_extract(m.meta_json,'$.scrape.poster')),'') IS NOT NULL OR NULLIF(TRIM(json_extract(m.meta_json,'$.scrape.extra.poster')),'') IS NOT NULL OR CAST(NULLIF(json_extract(m.meta_json,'$.scrape.rating'),'') AS REAL)>0 OR NULLIF(TRIM(json_extract(m.meta_json,'$.scrape.release_date')),'') IS NOT NULL OR NULLIF(TRIM(json_extract(m.meta_json,'$.scrape.extra.tmdb_id')),'') IS NOT NULL OR NULLIF(TRIM(json_extract(m.meta_json,'$.scrape.extra.imdb_id')),'') IS NOT NULL) THEN 1 ELSE 0 END,
CASE WHEN mea.status='encrypted' OR lower(m.file_path) LIKE '%.enc' THEN 1 ELSE 0 END,
` + optimizationAssetRecordedSQL + `,` + keyExpr + `,m.publication_state,m.published_at,m.publication_error,m.ingest_generation,` + ingestRunStatusSelect(spec.IncludeUnpublished) + `
FROM candidates m
LEFT JOIN pmax ON pmax.file_id=m.file_id
LEFT JOIN pu ON pu.file_id=m.file_id
LEFT JOIN mt_pick ON mt_pick.media_id=m.id
LEFT JOIN music_track mt ON mt.id=mt_pick.track_id
LEFT JOIN music_album ma ON ma.id=mt.album_id
LEFT JOIN music_artist ar ON ar.id=ma.album_artist_id
LEFT JOIN media_encrypted_assets mea ON mea.media_id=m.id
LEFT JOIN library l ON l.id=m.library_id
` + ingestRunStatusJoin(spec.IncludeUnpublished) + `ORDER BY ` + orderBy
	needs := false
	for _, folders := range spec.FolderScope {
		if len(folders) > 0 {
			needs = true
			break
		}
	}
	if spec.PhotoTag != "" && spec.PhotoTag != "all" {
		needs = true
	}
	return mediaQuery{SQL: q, Args: args, NeedsGoFilter: needs}, nil
}

func ingestRunStatusSelect(includeUnpublished bool) string {
	if includeUnpublished {
		return "COALESCE(mir.status,'')"
	}
	return "''"
}

func ingestRunStatusJoin(includeUnpublished bool) string {
	if includeUnpublished {
		return "LEFT JOIN media_ingest_run mir ON mir.media_id=m.id AND mir.generation=m.ingest_generation\n"
	}
	return ""
}

func (h *Handler) queryMediaBatch(ctx context.Context, q mediaQuery) ([]mediaListRow, error) {
	db := q.queryer
	if db == nil {
		db = h.App.DB
	}
	rows, err := db.QueryContext(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]mediaListRow, 0)
	for rows.Next() {
		var r mediaListRow
		if err := rows.Scan(&r.ID, &r.LibraryID, &r.FileID, &r.Title, &r.OriginalTitle, &r.FilePath, &r.FileType, &r.Duration, &r.Width, &r.Height, &r.Bitrate, &r.Format, &r.Status, &r.CreatedAt, &r.LastPlayAt, &r.PlayCompleted, &r.ReleaseDate, &r.ReleaseYear, &r.PosterURL, &r.BackdropURL, &r.PhotoTakenAt, &r.PhotoTagsRaw, &r.MusicAlbumID, &r.MusicAlbumTitle, &r.MusicArtist, &r.Scraped, &r.EncryptedAsset, &r.OptimizationAssetRecorded, &r.SortKey, &r.PublicationState, &r.PublishedAt, &r.PublicationError, &r.IngestGeneration, &r.IngestRunStatus); err != nil {
			return nil, err
		}
		r.PhotoTags = parseJSONStringArray(r.PhotoTagsRaw.String)
		r.PhotoTagIDs = photoclass.TagIDs(r.PhotoTags)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
func cursorFrom(r mediaListRow) *mediaCursor {
	cursor := &mediaCursor{ID: r.ID}
	if r.SortKey.Valid {
		value := r.SortKey.String
		cursor.SortKey = &value
	}
	return cursor
}
func (h *Handler) listMediaRows(ctx context.Context, spec mediaListSpec) ([]mediaListRow, mediaListStats, error) {
	return h.listMediaRowsObserved(ctx, spec, nil)
}

func (h *Handler) listMediaRowsObserved(ctx context.Context, spec mediaListSpec, afterBatch func(mediaListStats)) ([]mediaListRow, mediaListStats, error) {
	var stats mediaListStats
	items := make([]mediaListRow, 0, spec.Limit)
	cursor := spec.Cursor
	for len(items) < spec.Limit {
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		n := spec.Limit - len(items)
		if n > spec.BatchSize || hasGoFilters(spec) {
			n = spec.BatchSize
		}
		q, err := buildMediaQuery(spec, cursor, n)
		if err != nil {
			return nil, stats, err
		}
		q.queryer = h.App.DB
		batch, err := h.queryMediaBatch(ctx, q)
		if err != nil {
			return nil, stats, err
		}
		stats.Batches++
		stats.Candidates += len(batch)
		if afterBatch != nil {
			afterBatch(stats)
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			if folders := spec.FolderScope[r.LibraryID.Int64]; len(folders) > 0 && !pathMatchesAnyFolder(r.FilePath.String, folders) {
				stats.Rejected++
				continue
			}
			if !photoTagIDMatches(spec.PhotoTag, r.PhotoTags, r.PhotoTagIDs) {
				stats.Rejected++
				continue
			}
			items = append(items, r)
			if len(items) == spec.Limit {
				break
			}
		}
		cursor = cursorFrom(batch[len(batch)-1])
		if len(batch) < n {
			break
		}
	}
	stats.Returned = len(items)
	if err := ctx.Err(); err != nil {
		return nil, stats, err
	}
	return items, stats, nil
}
func hasGoFilters(spec mediaListSpec) bool {
	if spec.PhotoTag != "" && spec.PhotoTag != "all" {
		return true
	}
	for _, f := range spec.FolderScope {
		if len(f) > 0 {
			return true
		}
	}
	return false
}
