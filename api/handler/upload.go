package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"knox-media/pkg/ffprobe"
	"knox-media/pkg/fileutil"
	"knox-media/pkg/hashutil"
)

func (h *Handler) UploadImage(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file required"})
		return
	}
	ext := filepath.Ext(filepath.Base(fh.Filename))
	if ext == "" {
		ext = ".jpg"
	}
	name := "img-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ext
	dest := filepath.Join(h.Upload.UploadDir, name)
	if err := c.SaveUploadedFile(fh, dest); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := os.Stat(dest); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":   true,
		"path": dest,
		"url":  "/uploads/" + name,
	})
}

func (h *Handler) UploadSingle(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file required"})
		return
	}
	name := filepath.Base(fh.Filename)
	if name == "" || name == "." {
		name = "upload.bin"
	}
	libID := c.PostForm("library_id")
	targetDir := strings.TrimSpace(c.PostForm("target_dir"))
	destDir, lid, err := h.resolveUploadTargetDir(libID, targetDir)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	dest := filepath.Join(destDir, name)
	if err := c.SaveUploadedFile(fh, dest); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	md5, _ := hashutil.MD5File(dest)
	ft := fileutil.GuessFileType(name)
	dur, w, hh, br, format, metaJSON := probeMediaMeta(h.App.Config.FFmpeg.FFprobePath, dest, ft, name)
	fileID := uuid.NewString()
	title := c.PostForm("title")
	if title == "" {
		title = name
	}
	var lib any
	if lid != nil && lid.Valid {
		lib = lid.Int64
	}
	res, err := h.App.DB.Exec(`
		INSERT INTO media (library_id, file_id, title, file_path, file_type, duration, width, height, bitrate, md5, format, meta_json, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active')`,
		lib, fileID, title, dest, ft, dur, w, hh, br, nullStr(md5), format, metaJSON,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	mid, _ := res.LastInsertId()
	h.enqueuePackageTaskForUploadedMedia(mid, ft, lid)
	if ft == "video" {
		go h.KickIngestJITPrepare(mid)
	}
	h.EnqueuePostIngestForNewMedia(mid, ft)
	c.JSON(http.StatusOK, gin.H{"id": mid, "file_id": fileID, "path": dest, "md5": md5})
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func probeMediaMeta(ffprobePath, mediaPath, fileType, title string) (dur, w, h, br any, format any, metaJSON string) {
	if fileType == "video" || fileType == "audio" {
		if pr, err := ffprobe.Probe(ffprobePath, mediaPath); err == nil {
			dur = nullInt(pr.DurationSec)
			w = nullInt(pr.Width)
			h = nullInt(pr.Height)
			br = nullInt(pr.Bitrate)
			format = nullStr(pr.Format)
			metaJSON = pr.RawJSON
		}
	}
	if metaJSON == "" {
		b, _ := json.Marshal(map[string]string{"title": title})
		metaJSON = string(b)
	}
	return
}

func (h *Handler) UploadChunk(c *gin.Context) {
	uploadID := c.PostForm("upload_id")
	if uploadID == "" {
		uploadID = uuid.NewString()
	}
	idx, err := strconv.Atoi(c.PostForm("index"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "index required"})
		return
	}
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file chunk required"})
		return
	}
	src, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer src.Close()
	if _, err := h.Upload.SaveChunk(uploadID, idx, src); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"upload_id": uploadID, "index": idx})
}

type mergeBody struct {
	UploadID   string `json:"upload_id" binding:"required"`
	Filename   string `json:"filename" binding:"required"`
	TotalParts int    `json:"total_parts" binding:"required"`
	LibraryID  *int64 `json:"library_id"`
	TargetDir  string `json:"target_dir"`
	Title      string `json:"title"`
}

func (h *Handler) UploadMerge(c *gin.Context) {
	var body mergeBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	base := filepath.Base(body.Filename)
	if base == "" || base == "." {
		base = "merged.bin"
	}
	target := strings.TrimSpace(body.TargetDir)
	destDir, lid, err := h.resolveUploadTargetDir(strconv.FormatInt(defaultInt64(body.LibraryID), 10), target)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	path, sha, err := h.Upload.Merge(body.UploadID, body.TotalParts, filepath.Join(destDir, base))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	md5, _ := hashutil.MD5File(path)
	ft := fileutil.GuessFileType(base)
	dur, w, hh, br, format, metaJSON := probeMediaMeta(h.App.Config.FFmpeg.FFprobePath, path, ft, base)
	fileID := uuid.NewString()
	title := body.Title
	if title == "" {
		title = base
	}
	var lib any
	if body.LibraryID != nil {
		lib = *body.LibraryID
	}
	res, err := h.App.DB.Exec(`
		INSERT INTO media (library_id, file_id, title, file_path, file_type, duration, width, height, bitrate, md5, format, meta_json, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active')`,
		lib, fileID, title, path, ft, dur, w, hh, br, nullStr(md5), format, metaJSON,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	mid, _ := res.LastInsertId()
	h.enqueuePackageTaskForUploadedMedia(mid, ft, lid)
	if ft == "video" {
		go h.KickIngestJITPrepare(mid)
	}
	h.EnqueuePostIngestForNewMedia(mid, ft)
	c.JSON(http.StatusOK, gin.H{"id": mid, "file_id": fileID, "path": path, "sha256": sha, "md5": md5})
}

func (h *Handler) enqueuePackageTaskForUploadedMedia(mediaID int64, fileType string, lid *sql.NullInt64) {
	if h == nil || h.PackageWorker == nil || mediaID <= 0 || fileType != "video" {
		return
	}
	if lid == nil || !lid.Valid || lid.Int64 <= 0 {
		return
	}
	var drm int
	if err := h.App.DB.QueryRow(`SELECT COALESCE(drm_enabled,0) FROM library WHERE id = ?`, lid.Int64).Scan(&drm); err != nil || drm == 1 {
		return
	}
	go func(id int64) {
		_, _ = h.PackageWorker.EnqueueForMedia(id)
	}(mediaID)
}

type mkdirBody struct {
	LibraryID *int64 `json:"library_id"`
	TargetDir string `json:"target_dir"`
	Name      string `json:"name" binding:"required"`
}

func (h *Handler) CreateUploadDirectory(c *gin.Context) {
	var body mkdirBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid directory name"})
		return
	}
	destDir, _, err := h.resolveUploadTargetDir(strconv.FormatInt(defaultInt64(body.LibraryID), 10), strings.TrimSpace(body.TargetDir))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	created := filepath.Join(destDir, name)
	if err := os.MkdirAll(created, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "path": created})
}

func defaultInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func (h *Handler) resolveUploadTargetDir(rawLibraryID, rawTargetDir string) (string, *sql.NullInt64, error) {
	targetDir := strings.TrimSpace(rawTargetDir)
	if rawLibraryID == "" || rawLibraryID == "0" {
		base, err := filepath.Abs(h.Upload.UploadDir)
		if err != nil {
			return "", nil, err
		}
		if targetDir == "" {
			return base, nil, nil
		}
		if filepath.IsAbs(targetDir) {
			return "", nil, errors.New("target_dir must be relative when library_id is empty")
		}
		finalDir := filepath.Clean(filepath.Join(base, targetDir))
		rel, err := filepath.Rel(base, finalDir)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", nil, errors.New("invalid target_dir")
		}
		return finalDir, nil, nil
	}
	lid, err := strconv.ParseInt(rawLibraryID, 10, 64)
	if err != nil || lid <= 0 {
		return "", nil, errors.New("invalid library_id")
	}
	var root string
	if err := h.App.DB.QueryRow(`SELECT path FROM library WHERE id = ? LIMIT 1`, lid).Scan(&root); err != nil {
		if err == sql.ErrNoRows {
			return "", nil, errors.New("library not found")
		}
		return "", nil, err
	}
	roots := listLibraryFolders(h.App.DB, lid, root)
	if len(roots) == 0 {
		return "", nil, errors.New("library folders not found")
	}
	baseAbs := make([]string, 0, len(roots))
	for _, r := range roots {
		a, e := filepath.Abs(r)
		if e == nil {
			baseAbs = append(baseAbs, filepath.Clean(a))
		}
	}
	if len(baseAbs) == 0 {
		return "", nil, errors.New("library folders not found")
	}
	var finalDir string
	if targetDir == "" {
		finalDir = baseAbs[0]
	} else {
		if !filepath.IsAbs(targetDir) {
			return "", nil, errors.New("target_dir must be absolute path")
		}
		a, e := filepath.Abs(targetDir)
		if e != nil {
			return "", nil, e
		}
		finalDir = filepath.Clean(a)
	}
	inRoots := false
	for _, b := range baseAbs {
		rel, e := filepath.Rel(b, finalDir)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			inRoots = true
			break
		}
	}
	if !inRoots {
		return "", nil, errors.New("target_dir is outside library folders")
	}
	lib := &sql.NullInt64{Int64: lid, Valid: true}
	return finalDir, lib, nil
}
