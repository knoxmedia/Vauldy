package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type adminUserBody struct {
	Username          string  `json:"username"`
	Password          string  `json:"password"`
	Role              string  `json:"role"`
	CanManage         *int    `json:"can_manage"`
	CanPlay           *int    `json:"can_play"`
	CanDownload       *int    `json:"can_download"`
	CanAccessFeatures *int    `json:"can_access_features"`
	LibraryScope      string  `json:"library_scope"`
	LibraryIDs        []int64 `json:"library_ids"`
	LibraryFolders    map[string][]string `json:"library_folders"`
	ParentalEnabled   *int    `json:"parental_enabled"`
	ParentalMaxRating string  `json:"parental_max_rating"`
	ParentalPIN       string  `json:"parental_pin"`
	AllowedTimeStart  string  `json:"allowed_time_start"`
	AllowedTimeEnd    string  `json:"allowed_time_end"`
	ParentalPlans     []parentalAccessPlan `json:"parental_plans"`
}

func (h *Handler) ListUsersAdmin(c *gin.Context) {
	rows, err := h.App.DB.Query(`
		SELECT id, username, role, COALESCE(can_manage,0), COALESCE(can_play,1), COALESCE(can_download,0), COALESCE(can_access_features,1),
		       COALESCE(library_scope,'all'), COALESCE(parental_enabled,0), COALESCE(parental_max_rating,''), COALESCE(allowed_time_start,''), COALESCE(allowed_time_end,''),
			   COALESCE(parental_access_plan_json,'[]')
		FROM user ORDER BY id
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id int64
		var username, role, libraryScope, parentalMaxRating, allowedTimeStart, allowedTimeEnd, parentalPlansRaw string
		var canManage, canPlay, canDownload, canAccessFeatures, parentalEnabled int
		if rows.Scan(&id, &username, &role, &canManage, &canPlay, &canDownload, &canAccessFeatures, &libraryScope, &parentalEnabled, &parentalMaxRating, &allowedTimeStart, &allowedTimeEnd, &parentalPlansRaw) != nil {
			continue
		}
		parentalPlans := parseParentalPlansJSON(parentalPlansRaw)
		libraryIDs := []int64{}
		libraryFolders := map[string][]string{}
		if strings.EqualFold(libraryScope, "selected") {
			libraryIDs = h.userLibraryIDs(id)
			libraryFolders = h.userLibraryFolders(id)
		}
		items = append(items, gin.H{
			"id": id, "username": username, "role": role,
			"can_manage": canManage, "can_play": canPlay, "can_download": canDownload, "can_access_features": canAccessFeatures,
			"library_scope": libraryScope, "library_ids": libraryIDs,
			"library_folders": libraryFolders,
			"parental_enabled": parentalEnabled, "parental_max_rating": parentalMaxRating,
			"allowed_time_start": allowedTimeStart, "allowed_time_end": allowedTimeEnd,
			"parental_plans": parentalPlans,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) CreateUserAdmin(c *gin.Context) {
	var body adminUserBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	username := strings.TrimSpace(body.Username)
	if username == "" || len(body.Password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password(min 6) required"})
		return
	}
	role := normalizeRole(body.Role)
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	pinHash := ""
	if strings.TrimSpace(body.ParentalPIN) != "" {
		hh, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(body.ParentalPIN)), bcrypt.DefaultCost)
		if err == nil {
			pinHash = string(hh)
		}
	}
	canManage := intOrDefault(body.CanManage, 0)
	canPlay := intOrDefault(body.CanPlay, 1)
	canDownload := intOrDefault(body.CanDownload, 0)
	canAccessFeatures := intOrDefault(body.CanAccessFeatures, 1)
	parentalEnabled := intOrDefault(body.ParentalEnabled, 0)
	libraryScope := normalizeLibraryScope(body.LibraryScope)
	if role == "admin" {
		canManage, canPlay, canDownload, canAccessFeatures = 1, 1, 1, 1
		libraryScope = "all"
		parentalEnabled = 0
	}
	parentalPlansJSON := parentalPlansToJSON(body.ParentalPlans)
	if hasParentalPlanConflict(normalizeParentalPlans(body.ParentalPlans)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "parental plans conflict: overlapping schedules on same weekday"})
		return
	}
	if libraryScope == "selected" {
		if merged := mergeLibraryIDsForSelected(body.LibraryIDs, body.LibraryFolders); len(merged) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "library_scope selected requires at least one media library"})
			return
		}
	}
	res, err := h.App.DB.Exec(`
		INSERT INTO user (username, password, role, can_manage, can_play, can_download, can_access_features, library_scope, parental_enabled, parental_max_rating, parental_pin_hash, allowed_time_start, allowed_time_end, parental_access_plan_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, username, string(passwordHash), role, canManage, canPlay, canDownload, canAccessFeatures, libraryScope, parentalEnabled, strings.TrimSpace(body.ParentalMaxRating), pinHash, normalizeHHMM(body.AllowedTimeStart), normalizeHHMM(body.AllowedTimeEnd), parentalPlansJSON)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, _ := res.LastInsertId()
	if libraryScope == "selected" {
		merged := mergeLibraryIDsForSelected(body.LibraryIDs, body.LibraryFolders)
		_ = h.replaceUserLibraryPermissions(id, merged)
		_ = h.replaceUserLibraryFolderPermissions(id, body.LibraryFolders)
	}
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

func (h *Handler) UpdateUserAdmin(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body adminUserBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	username := strings.TrimSpace(body.Username)
	if username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username required"})
		return
	}
	role := normalizeRole(body.Role)
	canManage := intOrDefault(body.CanManage, 0)
	canPlay := intOrDefault(body.CanPlay, 1)
	canDownload := intOrDefault(body.CanDownload, 0)
	canAccessFeatures := intOrDefault(body.CanAccessFeatures, 1)
	parentalEnabled := intOrDefault(body.ParentalEnabled, 0)
	libraryScope := normalizeLibraryScope(body.LibraryScope)
	if role == "admin" {
		canManage, canPlay, canDownload, canAccessFeatures = 1, 1, 1, 1
		libraryScope = "all"
		parentalEnabled = 0
	}
	parentalPlansJSON := parentalPlansToJSON(body.ParentalPlans)
	if hasParentalPlanConflict(normalizeParentalPlans(body.ParentalPlans)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "parental plans conflict: overlapping schedules on same weekday"})
		return
	}
	if libraryScope == "selected" {
		if merged := mergeLibraryIDsForSelected(body.LibraryIDs, body.LibraryFolders); len(merged) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "library_scope selected requires at least one media library"})
			return
		}
	}
	pinHashExpr := ""
	args := []any{username, role, canManage, canPlay, canDownload, canAccessFeatures, libraryScope, parentalEnabled, strings.TrimSpace(body.ParentalMaxRating), normalizeHHMM(body.AllowedTimeStart), normalizeHHMM(body.AllowedTimeEnd), parentalPlansJSON}
	if pin := strings.TrimSpace(body.ParentalPIN); pin != "" {
		hh, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
		if err == nil {
			pinHashExpr = ", parental_pin_hash = ?"
			args = append(args, string(hh))
		}
	}
	args = append(args, id)
	_, err = h.App.DB.Exec(`
		UPDATE user
		SET username = ?, role = ?, can_manage = ?, can_play = ?, can_download = ?, can_access_features = ?,
		    library_scope = ?, parental_enabled = ?, parental_max_rating = ?, allowed_time_start = ?, allowed_time_end = ?, parental_access_plan_json = ?`+pinHashExpr+`
		WHERE id = ?
	`, args...)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if libraryScope == "selected" {
		merged := mergeLibraryIDsForSelected(body.LibraryIDs, body.LibraryFolders)
		_ = h.replaceUserLibraryPermissions(id, merged)
		_ = h.replaceUserLibraryFolderPermissions(id, body.LibraryFolders)
	} else {
		_, _ = h.App.DB.Exec(`DELETE FROM user_library_permission WHERE user_id = ?`, id)
		_, _ = h.App.DB.Exec(`DELETE FROM user_library_folder_permission WHERE user_id = ?`, id)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) ResetUserPasswordAdmin(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(strings.TrimSpace(body.Password)) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password(min 6) required"})
		return
	}
	hh, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(body.Password)), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_, err = h.App.DB.Exec(`UPDATE user SET password = ? WHERE id = ?`, string(hh), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) DeleteUserAdmin(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var role string
	if err := h.App.DB.QueryRow(`SELECT role FROM user WHERE id = ?`, id).Scan(&role); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if strings.EqualFold(role, "admin") {
		var n int
		_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM user WHERE role = 'admin'`).Scan(&n)
		if n <= 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot delete last admin"})
			return
		}
	}
	_, _ = h.App.DB.Exec(`DELETE FROM user_library_permission WHERE user_id = ?`, id)
	_, _ = h.App.DB.Exec(`DELETE FROM user_library_folder_permission WHERE user_id = ?`, id)
	if _, err := h.App.DB.Exec(`DELETE FROM user WHERE id = ?`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func normalizeRole(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "admin") {
		return "admin"
	}
	return "user"
}

func normalizeLibraryScope(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "selected") {
		return "selected"
	}
	return "all"
}

// mergeLibraryIDsForSelected deduplicates library IDs from the payload and from library_folders map keys.
func mergeLibraryIDsForSelected(ids []int64, folders map[string][]string) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0)
	for _, lid := range ids {
		if lid <= 0 {
			continue
		}
		if _, ok := seen[lid]; ok {
			continue
		}
		seen[lid] = struct{}{}
		out = append(out, lid)
	}
	for k := range folders {
		lid, err := strconv.ParseInt(strings.TrimSpace(k), 10, 64)
		if err != nil || lid <= 0 {
			continue
		}
		if _, ok := seen[lid]; ok {
			continue
		}
		seen[lid] = struct{}{}
		out = append(out, lid)
	}
	return out
}

func intOrDefault(v *int, d int) int {
	if v == nil {
		return d
	}
	if *v > 0 {
		return 1
	}
	return 0
}

func normalizeHHMM(v string) string {
	v = strings.TrimSpace(v)
	if len(v) != 5 || v[2] != ':' {
		return ""
	}
	return v
}

func parentalPlansToJSON(plans []parentalAccessPlan) string {
	clean := normalizeParentalPlans(plans)
	if len(clean) == 0 {
		return "[]"
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func parseParentalPlansJSON(raw string) []parentalAccessPlan {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var plans []parentalAccessPlan
	if err := json.Unmarshal([]byte(raw), &plans); err != nil {
		return nil
	}
	return normalizeParentalPlans(plans)
}

func normalizeParentalPlans(in []parentalAccessPlan) []parentalAccessPlan {
	out := make([]parentalAccessPlan, 0, len(in))
	for _, p := range in {
		if p.Weekday < 0 || p.Weekday > 6 {
			continue
		}
		start := normalizeHHMM(p.StartTime)
		end := normalizeHHMM(p.EndTime)
		if start == "" || end == "" {
			continue
		}
		out = append(out, parentalAccessPlan{
			Weekday:   p.Weekday,
			StartTime: start,
			EndTime:   end,
		})
	}
	return out
}

func hasParentalPlanConflict(plans []parentalAccessPlan) bool {
	type seg struct {
		start int
		end   int
	}
	byDay := map[int][]seg{}
	parseHHMM := func(v string) (int, bool) {
		v = normalizeHHMM(v)
		if v == "" {
			return 0, false
		}
		h, err1 := strconv.Atoi(v[:2])
		m, err2 := strconv.Atoi(v[3:])
		if err1 != nil || err2 != nil {
			return 0, false
		}
		return h*60 + m, true
	}
	for _, p := range plans {
		s, ok1 := parseHHMM(p.StartTime)
		e, ok2 := parseHHMM(p.EndTime)
		if !ok1 || !ok2 {
			continue
		}
		if s == e {
			return true
		}
		if s < e {
			byDay[p.Weekday] = append(byDay[p.Weekday], seg{start: s, end: e})
			continue
		}
		byDay[p.Weekday] = append(byDay[p.Weekday], seg{start: s, end: 24 * 60})
		next := (p.Weekday + 1) % 7
		byDay[next] = append(byDay[next], seg{start: 0, end: e})
	}
	for _, segs := range byDay {
		for i := 0; i < len(segs); i++ {
			for j := i + 1; j < len(segs); j++ {
				if segs[i].start < segs[j].end && segs[j].start < segs[i].end {
					return true
				}
			}
		}
	}
	return false
}

func (h *Handler) userLibraryIDs(userID int64) []int64 {
	rows, err := h.App.DB.Query(`SELECT library_id FROM user_library_permission WHERE user_id = ? ORDER BY library_id`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]int64, 0)
	for rows.Next() {
		var lid int64
		if rows.Scan(&lid) == nil && lid > 0 {
			out = append(out, lid)
		}
	}
	return out
}

func (h *Handler) userLibraryFolders(userID int64) map[string][]string {
	rows, err := h.App.DB.Query(`SELECT library_id, folder_path FROM user_library_folder_permission WHERE user_id = ? ORDER BY library_id, folder_path`, userID)
	if err != nil {
		return map[string][]string{}
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var lid int64
		var folder string
		if rows.Scan(&lid, &folder) == nil && lid > 0 && strings.TrimSpace(folder) != "" {
			k := strconv.FormatInt(lid, 10)
			out[k] = append(out[k], strings.TrimSpace(folder))
		}
	}
	return out
}

func (h *Handler) replaceUserLibraryPermissions(userID int64, libraryIDs []int64) error {
	tx, err := h.App.DB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM user_library_permission WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, lid := range libraryIDs {
		if lid <= 0 {
			continue
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO user_library_permission (user_id, library_id) VALUES (?, ?)`, userID, lid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (h *Handler) replaceUserLibraryFolderPermissions(userID int64, libraryFolders map[string][]string) error {
	tx, err := h.App.DB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM user_library_folder_permission WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for k, folders := range libraryFolders {
		lid, e := strconv.ParseInt(strings.TrimSpace(k), 10, 64)
		if e != nil || lid <= 0 {
			continue
		}
		for _, f := range folders {
			fp := strings.TrimSpace(f)
			if fp == "" {
				continue
			}
			if _, err = tx.Exec(`INSERT OR IGNORE INTO user_library_folder_permission (user_id, library_id, folder_path) VALUES (?, ?, ?)`, userID, lid, fp); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func verifyPinHash(hash string, pin string) bool {
	if strings.TrimSpace(hash) == "" {
		return false
	}
	if strings.TrimSpace(pin) == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin)) == nil
}

