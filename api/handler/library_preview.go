package handler

import (
	"database/sql"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"knox-media/internal/doccover"
	"knox-media/internal/imagethumb"
	"knox-media/internal/metadatalib"
	"knox-media/internal/storage"
)

const (
	libraryPreviewVersion = 2
	// Portrait strips (2:3) packed edge-to-edge; main + reflection below.
	libraryPreviewSlots   = 4
	libraryPreviewMainH   = 360
	libraryPreviewReflect = 140
)

// libraryPreviewWidth derives strip width from height so posters keep 2:3 without letterboxing.
func libraryPreviewWidth() int {
	slotW := (libraryPreviewMainH * 2) / 3
	return slotW * libraryPreviewSlots
}

func libraryPreviewTotalH() int {
	return libraryPreviewMainH + libraryPreviewReflect
}

var libraryPreviewLocks sync.Map
var libraryPreviewPending sync.Map

const (
	libraryPreviewKindPoster       = "poster"
	libraryPreviewKindPhotoThumb   = "photo_thumb"
	libraryPreviewKindDocCover     = "doc_cover"
	libraryPreviewKindMusicArtwork = "music_artwork"
)

type libraryPreviewSource struct {
	mediaID   int64
	albumID   int64
	posterURL string
	kind      string
}

// scheduleLibraryPreviewRefresh regenerates the composite preview asynchronously.
func (h *Handler) scheduleLibraryPreviewRefresh(libraryID int64) {
	if h == nil || h.App == nil || h.App.DB == nil || libraryID <= 0 {
		return
	}
	if _, loaded := libraryPreviewPending.LoadOrStore(libraryID, true); loaded {
		return
	}
	go func(lid int64) {
		defer libraryPreviewPending.Delete(lid)
		h.refreshLibraryPreview(lid)
	}(libraryID)
}

func (h *Handler) refreshLibraryPreview(libraryID int64) {
	lockAny, _ := libraryPreviewLocks.LoadOrStore(libraryID, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	uploadDir := strings.TrimSpace(h.App.Config.Data.Upload)
	if uploadDir == "" {
		return
	}
	sources, err := h.latestLibraryPreviewSources(libraryID)
	if err != nil {
		log.Printf("library preview sources library=%d: %v", libraryID, err)
		return
	}
	outDir := filepath.Join(uploadDir, "library_previews")
	outFile := filepath.Join(outDir, fmt.Sprintf("%d_v%d.jpg", libraryID, libraryPreviewVersion))
	if len(sources) == 0 {
		_ = os.Remove(outFile)
		return
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Printf("library preview mkdir library=%d: %v", libraryID, err)
		return
	}
	tmpFile := outFile + ".tmp"
	if err := composeLibraryPreviewImage(h, sources, tmpFile); err != nil {
		log.Printf("library preview compose library=%d: %v", libraryID, err)
		_ = os.Remove(tmpFile)
		return
	}
	if err := os.Rename(tmpFile, outFile); err != nil {
		_ = os.Remove(tmpFile)
		log.Printf("library preview rename library=%d: %v", libraryID, err)
	}
}

func (h *Handler) latestLibraryPreviewSources(libraryID int64) ([]libraryPreviewSource, error) {
	libType := strings.ToLower(strings.TrimSpace(h.loadLibraryType(libraryID)))
	candidates, err := h.queryLibraryPreviewCandidates(libraryID, libType, libraryPreviewSlots*5)
	if err != nil {
		return nil, err
	}
	out := make([]libraryPreviewSource, 0, libraryPreviewSlots)
	for _, src := range candidates {
		if !h.previewSourceReady(src) {
			continue
		}
		out = append(out, src)
		if len(out) >= libraryPreviewSlots {
			break
		}
	}
	return out, nil
}

func (h *Handler) queryLibraryPreviewCandidates(libraryID int64, libType string, limit int) ([]libraryPreviewSource, error) {
	if limit <= 0 {
		limit = libraryPreviewSlots
	}
	switch libType {
	case "music":
		return h.queryMusicPreviewCandidates(libraryID, limit)
	case "photo":
		return h.queryPhotoPreviewCandidates(libraryID, limit)
	case "document":
		return h.queryDocumentPreviewCandidates(libraryID, limit)
	default:
		return h.queryVideoPreviewCandidates(libraryID, limit)
	}
}

func (h *Handler) queryVideoPreviewCandidates(libraryID int64, limit int) ([]libraryPreviewSource, error) {
	rows, err := h.App.DB.Query(`
		SELECT m.id,
			COALESCE(
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.poster')), ''),
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.extra.poster')), '')
			) AS poster_url
		FROM media m
		WHERE m.library_id = ? AND m.file_type = 'video' AND m.status = 'active'
		ORDER BY datetime(m.created_at) DESC, m.id DESC
		LIMIT ?`, libraryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPreviewPosterRows(rows)
}

func (h *Handler) queryPhotoPreviewCandidates(libraryID int64, limit int) ([]libraryPreviewSource, error) {
	rows, err := h.App.DB.Query(`
		SELECT m.id, ''
		FROM media m
		WHERE m.library_id = ? AND m.file_type = 'image' AND m.status = 'active'
		ORDER BY datetime(m.created_at) DESC, m.id DESC
		LIMIT ?`, libraryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []libraryPreviewSource
	for rows.Next() {
		var src libraryPreviewSource
		var poster sql.NullString
		if err := rows.Scan(&src.mediaID, &poster); err != nil {
			continue
		}
		src.posterURL = strings.TrimSpace(poster.String)
		src.kind = libraryPreviewKindPhotoThumb
		out = append(out, src)
	}
	return out, rows.Err()
}

func (h *Handler) queryDocumentPreviewCandidates(libraryID int64, limit int) ([]libraryPreviewSource, error) {
	rows, err := h.App.DB.Query(`
		SELECT m.id, ''
		FROM media m
		WHERE m.library_id = ? AND m.file_type = 'document' AND m.status = 'active'
		ORDER BY datetime(m.created_at) DESC, m.id DESC
		LIMIT ?`, libraryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []libraryPreviewSource
	for rows.Next() {
		var src libraryPreviewSource
		var poster sql.NullString
		if err := rows.Scan(&src.mediaID, &poster); err != nil {
			continue
		}
		src.kind = libraryPreviewKindDocCover
		out = append(out, src)
	}
	return out, rows.Err()
}

func (h *Handler) queryMusicPreviewCandidates(libraryID int64, limit int) ([]libraryPreviewSource, error) {
	rows, err := h.App.DB.Query(`
		SELECT a.id,
			COALESCE(NULLIF(TRIM(a.artwork_path), ''), ''),
			COALESCE((
				SELECT mt.media_id
				FROM music_track mt
				JOIN media m ON m.id = mt.media_id AND m.status = 'active'
				WHERE mt.album_id = a.id
				ORDER BY mt.sort_order ASC, mt.track_number ASC, mt.media_id ASC
				LIMIT 1
			), 0)
		FROM music_album a
		WHERE a.library_id = ?
		ORDER BY datetime(COALESCE(a.updated_at, a.created_at)) DESC, a.id DESC
		LIMIT ?`, libraryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []libraryPreviewSource
	for rows.Next() {
		var src libraryPreviewSource
		var artwork sql.NullString
		if err := rows.Scan(&src.albumID, &artwork, &src.mediaID); err != nil {
			continue
		}
		src.posterURL = strings.TrimSpace(artwork.String)
		src.kind = libraryPreviewKindMusicArtwork
		out = append(out, src)
	}
	return out, rows.Err()
}

func scanPreviewPosterRows(rows *sql.Rows) ([]libraryPreviewSource, error) {
	var out []libraryPreviewSource
	for rows.Next() {
		var src libraryPreviewSource
		var poster sql.NullString
		if err := rows.Scan(&src.mediaID, &poster); err != nil {
			continue
		}
		src.posterURL = strings.TrimSpace(poster.String)
		src.kind = libraryPreviewKindPoster
		out = append(out, src)
	}
	return out, rows.Err()
}

func (h *Handler) previewSourceReady(src libraryPreviewSource) bool {
	switch src.kind {
	case libraryPreviewKindPhotoThumb:
		return h.photoThumbReady(src.mediaID)
	case libraryPreviewKindDocCover:
		previewDir := ""
		if h.App != nil && h.App.Config != nil {
			previewDir = h.App.Config.Data.Preview
		}
		return doccover.CachedCover(h.App.DB, previewDir, h.derivedBaseDir(), src.mediaID, 0)
	case libraryPreviewKindMusicArtwork:
		if p := h.existingArtworkFile(src.posterURL); p != "" {
			return true
		}
		if src.albumID > 0 && artworkFileReady(h.albumArtworkCacheFile(src.albumID)) {
			return true
		}
		if src.mediaID > 0 {
			uploadDir := ""
			if h.App != nil && h.App.Config != nil {
				uploadDir = strings.TrimSpace(h.App.Config.Data.Upload)
			}
			return storage.ResolvePosterServePath(h.App.DB, uploadDir, src.mediaID) != ""
		}
		return false
	default:
		if src.posterURL != "" && h.resolvePosterFilePath(src.mediaID, src.posterURL) != "" {
			return true
		}
		return h.resolvePosterFilePath(src.mediaID, "") != ""
	}
}

func (h *Handler) photoThumbReady(mediaID int64) bool {
	if mediaID <= 0 {
		return false
	}
	paths := imagethumb.ResolvedPaths(h.App.DB, h.photoCacheDir(), mediaID)
	if artworkFileReady(paths.Thumb) {
		return true
	}
	if enc, ok := storage.LookupEncPath(h.App.DB, mediaID, "photo_thumb", "thumb.jpg"); ok {
		return artworkFileReady(enc)
	}
	return false
}

func (h *Handler) scheduleLibraryPreviewRefreshForMedia(mediaID int64) {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 {
		return
	}
	var libraryID sql.NullInt64
	if err := h.App.DB.QueryRow(`SELECT library_id FROM media WHERE id = ?`, mediaID).Scan(&libraryID); err != nil || libraryID.Int64 <= 0 {
		return
	}
	h.scheduleLibraryPreviewRefresh(libraryID.Int64)
}

// ScheduleLibraryPreviewRefreshForMedia regenerates the library strip after one media item gains a cover/thumb.
func (h *Handler) ScheduleLibraryPreviewRefreshForMedia(mediaID int64) {
	h.scheduleLibraryPreviewRefreshForMedia(mediaID)
}

func (h *Handler) libraryPreviewPublicURL(libraryID int64) string {
	uploadDir := strings.TrimSpace(h.App.Config.Data.Upload)
	if uploadDir == "" || libraryID <= 0 {
		return ""
	}
	outFile := filepath.Join(uploadDir, "library_previews", fmt.Sprintf("%d_v%d.jpg", libraryID, libraryPreviewVersion))
	st, err := os.Stat(outFile)
	if err != nil || st.IsDir() || st.Size() == 0 {
		return ""
	}
	return fmt.Sprintf("/uploads/library_previews/%d_v%d.jpg?v=%d", libraryID, libraryPreviewVersion, st.ModTime().Unix())
}

func (h *Handler) resolvePosterFilePath(mediaID int64, posterURL string) string {
	uploadDir := strings.TrimSpace(h.App.Config.Data.Upload)
	metaRoot := strings.TrimSpace(h.App.Config.Data.MetadataLibrary)
	posterURL = strings.TrimSpace(posterURL)

	try := func(abs string) string {
		st, err := os.Stat(abs)
		if err != nil || st.IsDir() || st.Size() == 0 {
			return ""
		}
		return abs
	}

	if posterURL != "" {
		switch {
		case strings.HasPrefix(posterURL, "/api/v1/media/") && strings.HasSuffix(posterURL, "/poster.jpg"):
			if enc, ok := storage.LookupEncPath(h.App.DB, mediaID, "poster", "poster.jpg"); ok {
				if p := try(enc); p != "" {
					return p
				}
			}
		case strings.HasPrefix(posterURL, "/uploads/") && uploadDir != "":
			if p := try(filepath.Join(uploadDir, filepath.FromSlash(strings.TrimPrefix(posterURL, "/uploads/")))); p != "" {
				return p
			}
		case metadatalib.IsLocalMetadataURL(posterURL) && metaRoot != "":
			rel := strings.TrimPrefix(posterURL, metadatalib.PublicURLPrefix+"/")
			if p := try(filepath.Join(metaRoot, filepath.FromSlash(rel))); p != "" {
				return p
			}
		}
	}
	if uploadDir != "" {
		if enc, ok := storage.LookupEncPath(h.App.DB, mediaID, "poster", "poster.jpg"); ok {
			if p := try(enc); p != "" {
				return p
			}
		}
		if p := try(filepath.Join(uploadDir, "posters", fmt.Sprintf("%d.jpg", mediaID))); p != "" {
			return p
		}
	}
	return ""
}

func (h *Handler) loadCoverImageForCompose(src libraryPreviewSource) (image.Image, error) {
	switch src.kind {
	case libraryPreviewKindPhotoThumb:
		return h.loadPhotoThumbForCompose(src.mediaID)
	case libraryPreviewKindDocCover:
		return h.loadDocCoverForCompose(src.mediaID)
	case libraryPreviewKindMusicArtwork:
		return h.loadMusicArtworkForCompose(src.albumID, src.mediaID, src.posterURL)
	default:
		return h.loadPosterImageForCompose(src.mediaID, src.posterURL)
	}
}

func (h *Handler) loadPhotoThumbForCompose(mediaID int64) (image.Image, error) {
	if enc, ok := storage.LookupEncPath(h.App.DB, mediaID, "photo_thumb", "thumb.jpg"); ok {
		seeker, err := storage.OpenDerivedSeeker(h.App.DB, h.KeyVault, mediaID, enc)
		if err != nil {
			return nil, err
		}
		defer seeker.Close()
		img, _, err := image.Decode(seeker)
		return img, err
	}
	paths := imagethumb.ResolvedPaths(h.App.DB, h.photoCacheDir(), mediaID)
	return loadImageFile(paths.Thumb)
}

func (h *Handler) loadDocCoverForCompose(mediaID int64) (image.Image, error) {
	if enc, ok := storage.ResolveDerivedEncPath(h.App.DB, h.derivedBaseDir(), mediaID, "doc_cover", "cover.jpg"); ok {
		seeker, err := storage.OpenDerivedArtifactSeeker(h.App.DB, h.KeyVault, mediaID, enc, "doc_cover", "cover.jpg")
		if err != nil {
			return nil, err
		}
		defer seeker.Close()
		img, _, err := image.Decode(seeker)
		return img, err
	}
	previewDir := ""
	if h.App != nil && h.App.Config != nil {
		previewDir = h.App.Config.Data.Preview
	}
	return loadImageFile(doccover.Path(previewDir, mediaID))
}

func (h *Handler) loadMusicArtworkForCompose(albumID, mediaID int64, artworkPath string) (image.Image, error) {
	if p := h.existingArtworkFile(artworkPath); p != "" {
		return loadImageFile(p)
	}
	if albumID > 0 {
		if p := h.albumArtworkCacheFile(albumID); artworkFileReady(p) {
			return loadImageFile(p)
		}
	}
	return h.loadPosterImageForCompose(mediaID, "")
}

func (h *Handler) loadPosterImageForCompose(mediaID int64, posterURL string) (image.Image, error) {
	if enc, ok := storage.LookupEncPath(h.App.DB, mediaID, "poster", "poster.jpg"); ok {
		seeker, err := storage.OpenDerivedSeeker(h.App.DB, h.KeyVault, mediaID, enc)
		if err != nil {
			return nil, err
		}
		defer seeker.Close()
		img, _, err := image.Decode(seeker)
		return img, err
	}
	path := h.resolvePosterFilePath(mediaID, posterURL)
	if path == "" {
		return nil, os.ErrNotExist
	}
	return loadImageFile(path)
}

func composeLibraryPreviewImage(h *Handler, sources []libraryPreviewSource, outFile string) error {
	width := libraryPreviewWidth()
	slotW := width / libraryPreviewSlots
	totalH := libraryPreviewTotalH()
	canvas := image.NewRGBA(image.Rect(0, 0, width, totalH))
	mainRect := image.Rect(0, 0, width, libraryPreviewMainH)
	draw.Draw(canvas, mainRect, &image.Uniform{C: color.RGBA{0x12, 0x12, 0x16, 0xff}}, image.Point{}, draw.Src)

	loaded := 0
	for i := 0; i < libraryPreviewSlots; i++ {
		x0 := i * slotW
		x1 := x0 + slotW
		if i == libraryPreviewSlots-1 {
			x1 = width
		}
		rect := image.Rect(x0, 0, x1, libraryPreviewMainH)
		if i < len(sources) {
			if img, err := h.loadCoverImageForCompose(sources[i]); err == nil {
				drawCover(canvas, img, rect)
				loaded++
				continue
			}
		}
		fillPlaceholderSlot(canvas, rect, i)
	}
	if loaded == 0 {
		return fmt.Errorf("no cover images loaded")
	}
	drawReflection(canvas, libraryPreviewMainH, libraryPreviewReflect)
	f, err := os.Create(outFile)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, canvas, &jpeg.Options{Quality: 90})
}

func loadImageFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

func drawCover(dst *image.RGBA, src image.Image, rect image.Rectangle) {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return
	}
	dw, dh := rect.Dx(), rect.Dy()
	if dw <= 0 || dh <= 0 {
		return
	}
	// Scale uniformly to cover the slot, then center-crop (never stretch).
	scale := math.Max(float64(dw)/float64(sw), float64(dh)/float64(sh))
	nw := int(math.Ceil(float64(sw) * scale))
	nh := int(math.Ceil(float64(sh) * scale))
	if nw < dw {
		nw = dw
	}
	if nh < dh {
		nh = dh
	}
	scaled := image.NewRGBA(image.Rect(0, 0, nw, nh))
	bilinearScale(scaled, src)
	sx := (nw - dw) / 2
	sy := (nh - dh) / 2
	draw.Draw(dst, rect, scaled, image.Pt(sx, sy), draw.Src)
}

func bilinearScale(dst *image.RGBA, src image.Image) {
	sb := src.Bounds()
	db := dst.Bounds()
	sw := float64(sb.Dx())
	sh := float64(sb.Dy())
	if sw <= 0 || sh <= 0 {
		return
	}
	for y := db.Min.Y; y < db.Max.Y; y++ {
		v := (float64(y-db.Min.Y) + 0.5) / float64(db.Dy()) * sh
		if v < 0 {
			v = 0
		}
		if v >= sh {
			v = sh - 1
		}
		y0 := int(math.Floor(v))
		y1 := y0 + 1
		if y1 >= int(sh) {
			y1 = int(sh) - 1
		}
		fy := v - float64(y0)
		for x := db.Min.X; x < db.Max.X; x++ {
			u := (float64(x-db.Min.X) + 0.5) / float64(db.Dx()) * sw
			if u < 0 {
				u = 0
			}
			if u >= sw {
				u = sw - 1
			}
			x0 := int(math.Floor(u))
			x1 := x0 + 1
			if x1 >= int(sw) {
				x1 = int(sw) - 1
			}
			fx := u - float64(x0)
			c00 := src.At(sb.Min.X+x0, sb.Min.Y+y0)
			c10 := src.At(sb.Min.X+x1, sb.Min.Y+y0)
			c01 := src.At(sb.Min.X+x0, sb.Min.Y+y1)
			c11 := src.At(sb.Min.X+x1, sb.Min.Y+y1)
			dst.Set(x, y, bilinearColor(c00, c10, c01, c11, fx, fy))
		}
	}
}

func bilinearColor(c00, c10, c01, c11 color.Color, fx, fy float64) color.RGBA {
	r00, g00, b00, a00 := c00.RGBA()
	r10, g10, b10, a10 := c10.RGBA()
	r01, g01, b01, a01 := c01.RGBA()
	r11, g11, b11, a11 := c11.RGBA()
	lerp := func(a, b float64, t float64) float64 { return a + (b-a)*t }
	r0 := lerp(float64(r00>>8), float64(r10>>8), fx)
	g0 := lerp(float64(g00>>8), float64(g10>>8), fx)
	b0 := lerp(float64(b00>>8), float64(b10>>8), fx)
	a0 := lerp(float64(a00>>8), float64(a10>>8), fx)
	r1 := lerp(float64(r01>>8), float64(r11>>8), fx)
	g1 := lerp(float64(g01>>8), float64(g11>>8), fx)
	b1 := lerp(float64(b01>>8), float64(b11>>8), fx)
	a1 := lerp(float64(a01>>8), float64(a11>>8), fx)
	return color.RGBA{
		R: uint8(math.Round(lerp(r0, r1, fy))),
		G: uint8(math.Round(lerp(g0, g1, fy))),
		B: uint8(math.Round(lerp(b0, b1, fy))),
		A: uint8(math.Round(lerp(a0, a1, fy))),
	}
}

func fillPlaceholderSlot(dst *image.RGBA, rect image.Rectangle, slot int) {
	base := []color.RGBA{
		{0x22, 0x24, 0x32, 0xff},
		{0x1e, 0x28, 0x34, 0xff},
		{0x24, 0x1e, 0x30, 0xff},
		{0x1a, 0x26, 0x2a, 0xff},
	}
	c := base[slot%len(base)]
	draw.Draw(dst, rect, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

func drawReflection(canvas *image.RGBA, mainH, reflectH int) {
	if reflectH <= 0 {
		return
	}
	bg := color.RGBA{0x14, 0x14, 0x18, 0xff}
	for y := 0; y < reflectH; y++ {
		srcY := mainH - 1 - (y*mainH)/reflectH
		alpha := 0.45 * (1 - float64(y)/float64(reflectH))
		for x := 0; x < canvas.Bounds().Dx(); x++ {
			src := canvas.RGBAAt(x, srcY)
			canvas.SetRGBA(x, mainH+y, blendRGBA(bg, src, alpha))
		}
	}
}

func blendRGBA(bg, src color.RGBA, alpha float64) color.RGBA {
	if alpha <= 0 {
		return bg
	}
	if alpha >= 1 {
		return src
	}
	inv := 1 - alpha
	return color.RGBA{
		R: uint8(float64(src.R)*alpha + float64(bg.R)*inv),
		G: uint8(float64(src.G)*alpha + float64(bg.G)*inv),
		B: uint8(float64(src.B)*alpha + float64(bg.B)*inv),
		A: 255,
	}
}
