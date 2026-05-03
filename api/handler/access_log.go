package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func (h *Handler) ListAccessLogs(c *gin.Context) {
	limit := 100
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	action := strings.TrimSpace(c.Query("action"))
	rangePreset := strings.TrimSpace(c.Query("range"))
	if rangePreset == "" {
		rangePreset = "7d"
	}
	from := strings.TrimSpace(c.Query("from"))
	to := strings.TrimSpace(c.Query("to"))
	where := []string{"action IN ('login','logout','playback_start','playback_end')"}
	args := []any{}
	if action != "" && action != "all" {
		where = append(where, "action = ?")
		args = append(args, action)
	}
	switch rangePreset {
	case "today":
		where = append(where, "datetime(created_at) >= datetime('now','start of day')")
	case "7d":
		where = append(where, "datetime(created_at) >= datetime('now','-7 days')")
	case "30d":
		where = append(where, "datetime(created_at) >= datetime('now','-30 days')")
	case "custom":
		if from != "" {
			where = append(where, "datetime(created_at) >= datetime(?)")
			args = append(args, from)
		}
		if to != "" {
			where = append(where, "datetime(created_at) <= datetime(?)")
			args = append(args, to)
		}
	}
	q := fmt.Sprintf(`
		SELECT id, COALESCE(username,''), action, COALESCE(media_id,0), COALESCE(message,''), created_at
		FROM activity_log
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
		var id sql.NullInt64
		var username, itemAction, message, createdAt sql.NullString
		var mediaID sql.NullInt64
		if rows.Scan(&id, &username, &itemAction, &mediaID, &message, &createdAt) != nil {
			continue
		}
		items = append(items, gin.H{
			"id":         id.Int64,
			"username":   username.String,
			"action":     itemAction.String,
			"media_id":   mediaID.Int64,
			"message":    message.String,
			"created_at": createdAt.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
