package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func (h *Handler) ListDRMLicenseAudits(c *gin.Context) {
	limit := 100
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	where := []string{"1=1"}
	args := make([]any, 0, 4)

	if v := strings.TrimSpace(c.Query("media_id")); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			where = append(where, "media_id = ?")
			args = append(args, id)
		}
	}
	if v := strings.TrimSpace(c.Query("drm_type")); v != "" && v != "all" {
		where = append(where, "drm_type = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(c.Query("result")); v != "" && v != "all" {
		where = append(where, "result = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(c.Query("reason")); v != "" {
		where = append(where, "reason LIKE ?")
		args = append(args, "%"+v+"%")
	}
	rangePreset := strings.TrimSpace(c.Query("range"))
	if rangePreset == "" {
		rangePreset = "all"
	}
	switch rangePreset {
	case "today":
		where = append(where, "datetime(created_at) >= datetime('now','start of day')")
	case "7d":
		where = append(where, "datetime(created_at) >= datetime('now','-7 days')")
	case "30d":
		where = append(where, "datetime(created_at) >= datetime('now','-30 days')")
	case "custom":
		if v := strings.TrimSpace(c.Query("from")); v != "" {
			where = append(where, "datetime(created_at) >= datetime(?)")
			args = append(args, v)
		}
		if v := strings.TrimSpace(c.Query("to")); v != "" {
			where = append(where, "datetime(created_at) <= datetime(?)")
			args = append(args, v)
		}
	}

	q := fmt.Sprintf(`
		SELECT id, COALESCE(media_id,0), COALESCE(drm_type,''), COALESCE(result,''), COALESCE(reason,''), COALESCE(client_ip,''), created_at
		FROM drm_license_audit
		WHERE %s
		ORDER BY id DESC
		LIMIT ?
	`, strings.Join(where, " AND "))
	args = append(args, limit)

	rows, err := h.App.DB.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var id, mediaID int64
		var drmType, result, reason, clientIP, createdAt string
		if err := rows.Scan(&id, &mediaID, &drmType, &result, &reason, &clientIP, &createdAt); err != nil {
			continue
		}
		items = append(items, gin.H{
			"id":         id,
			"media_id":   mediaID,
			"drm_type":   drmType,
			"result":     result,
			"reason":     reason,
			"client_ip":  clientIP,
			"created_at": createdAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
