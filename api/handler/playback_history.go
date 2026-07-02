package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
)

func (h *Handler) ListPlaybackHistory(c *gin.Context) {
	limit := 200
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	rangePreset := strings.TrimSpace(c.Query("range"))
	if rangePreset == "" {
		rangePreset = "all"
	}

	mediaID := int64(0)
	if v := strings.TrimSpace(c.Query("media_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			mediaID = n
		}
	}

	libraryID := int64(0)
	if v := strings.TrimSpace(c.Query("library_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			libraryID = n
		}
	}

	isAdmin := middleware.IsAdmin(c)
	uid := middleware.UserID(c)

	filterUserID := int64(0)
	if isAdmin {
		if v := strings.TrimSpace(c.Query("user_id")); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				filterUserID = n
			}
		}
	} else if uid > 0 {
		filterUserID = uid
	}

	logWhere, logArgs := buildPlaybackLogFilters(filterUserID, mediaID, libraryID, rangePreset, "a", "m")
	progressWhere, progressArgs := buildPlaybackProgressFilters(filterUserID, mediaID, libraryID, rangePreset)

	q := fmt.Sprintf(`
		SELECT id, user_id, username, media_id, message, played_at, title, file_type, library_type, library_id
		FROM (
			SELECT a.id AS id,
			       COALESCE(a.user_id, 0) AS user_id,
			       COALESCE(NULLIF(a.username, ''), COALESCE(u.username, '')) AS username,
			       COALESCE(a.media_id, 0) AS media_id,
			       COALESCE(a.message, '') AS message,
			       a.created_at AS played_at,
			       COALESCE(m.title, '') AS title,
			       COALESCE(m.file_type, '') AS file_type,
			       COALESCE(l.type, '') AS library_type,
			       COALESCE(l.id, 0) AS library_id
			FROM activity_log a
			LEFT JOIN media m ON m.id = a.media_id
			LEFT JOIN library l ON l.id = m.library_id
			LEFT JOIN user u ON u.id = a.user_id
			WHERE a.action = 'playback_start' AND %s

			UNION ALL

			SELECT p.id AS id,
			       COALESCE(p.user_id, 0) AS user_id,
			       COALESCE(u.username, '') AS username,
			       m.id AS media_id,
			       COALESCE((
			           SELECT al.message FROM activity_log al
			           WHERE al.user_id = p.user_id AND al.media_id = m.id
			             AND al.action IN ('playback_start', 'playback_end')
			           ORDER BY al.id DESC LIMIT 1
			       ), '') AS message,
			       COALESCE(NULLIF(p.play_start_at, ''), p.update_at) AS played_at,
			       COALESCE(m.title, '') AS title,
			       COALESCE(m.file_type, '') AS file_type,
			       COALESCE(l.type, '') AS library_type,
			       COALESCE(l.id, 0) AS library_id
			FROM play_progress p
			INNER JOIN media m ON m.file_id = p.file_id
			LEFT JOIN user u ON u.id = p.user_id
			LEFT JOIN library l ON l.id = m.library_id
			WHERE %s
			  AND NOT EXISTS (
			      SELECT 1 FROM activity_log a2
			      WHERE a2.action = 'playback_start'
			        AND a2.user_id = p.user_id
			        AND a2.media_id = m.id
			  )
		)
		ORDER BY datetime(played_at) DESC, id DESC
		LIMIT ?
	`, logWhere, progressWhere)

	args := append(append([]any{}, logArgs...), progressArgs...)
	args = append(args, limit)

	rows, err := h.App.DB.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var id, userID, itemMediaID, libID sql.NullInt64
		var username, message, playedAt, title, fileType, libType sql.NullString
		if rows.Scan(&id, &userID, &username, &itemMediaID, &message, &playedAt, &title, &fileType, &libType, &libID) != nil {
			continue
		}
		player, platform := parsePlaybackUserAgent(message.String)
		items = append(items, gin.H{
			"id":           id.Int64,
			"user_id":      userID.Int64,
			"username":     username.String,
			"media_id":     itemMediaID.Int64,
			"title":        title.String,
			"file_type":    fileType.String,
			"library_id":   libID.Int64,
			"library_type": libType.String,
			"player":       player,
			"platform":     platform,
			"played_at":    playedAt.String,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
		"range": rangePreset,
	})
}

func buildPlaybackLogFilters(filterUserID, mediaID, libraryID int64, rangePreset, logAlias, mediaAlias string) (string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if filterUserID > 0 {
		where = append(where, fmt.Sprintf("%s.user_id = ?", logAlias))
		args = append(args, filterUserID)
	}
	if mediaID > 0 {
		where = append(where, fmt.Sprintf("%s.media_id = ?", logAlias))
		args = append(args, mediaID)
	}
	if libraryID > 0 {
		where = append(where, fmt.Sprintf("%s.library_id = ?", mediaAlias))
		args = append(args, libraryID)
	}
	where = append(where, playbackTimeFilter(rangePreset, logAlias+".created_at")...)
	return strings.Join(where, " AND "), args
}

func buildPlaybackProgressFilters(filterUserID, mediaID, libraryID int64, rangePreset string) (string, []any) {
	where := []string{
		`(p.position > 0 OR COALESCE(p.play_count, 0) > 0 OR COALESCE(p.completed, 0) = 1
		  OR (p.play_start_at IS NOT NULL AND TRIM(p.play_start_at) != ''))`,
	}
	args := []any{}
	if filterUserID > 0 {
		where = append(where, "p.user_id = ?")
		args = append(args, filterUserID)
	}
	if mediaID > 0 {
		where = append(where, "m.id = ?")
		args = append(args, mediaID)
	}
	if libraryID > 0 {
		where = append(where, "m.library_id = ?")
		args = append(args, libraryID)
	}
	playedAtExpr := `COALESCE(NULLIF(p.play_start_at, ''), p.update_at)`
	for _, clause := range playbackTimeFilter(rangePreset, playedAtExpr) {
		where = append(where, clause)
	}
	return strings.Join(where, " AND "), args
}

func playbackTimeFilter(rangePreset, timeExpr string) []string {
	switch rangePreset {
	case "7d":
		return []string{fmt.Sprintf("datetime(%s) >= datetime('now','-7 days')", timeExpr)}
	case "30d":
		return []string{fmt.Sprintf("datetime(%s) >= datetime('now','-30 days')", timeExpr)}
	case "90d":
		return []string{fmt.Sprintf("datetime(%s) >= datetime('now','-90 days')", timeExpr)}
	case "1y":
		return []string{fmt.Sprintf("datetime(%s) >= datetime('now','-1 year')", timeExpr)}
	default:
		return nil
	}
}

func parsePlaybackUserAgent(message string) (player, platform string) {
	ua := strings.ToLower(message)
	if idx := strings.Index(ua, "ua="); idx >= 0 {
		ua = ua[idx+3:]
	} else {
		ua = strings.ToLower(message)
	}
	ua = strings.TrimSpace(ua)

	switch {
	case strings.Contains(ua, "edg/"):
		player = "Microsoft Edge"
		platform = "Microsoft Edge"
	case strings.Contains(ua, "edge/"):
		player = "Microsoft Edge"
		platform = "Microsoft Edge"
	case strings.Contains(ua, "chrome/") && !strings.Contains(ua, "edg/"):
		player = "Chrome"
		platform = "Chrome"
	case strings.Contains(ua, "firefox/"):
		player = "Firefox"
		platform = "Firefox"
	case strings.Contains(ua, "safari/") && !strings.Contains(ua, "chrome/"):
		player = "Safari"
		platform = "Safari"
	default:
		player = "-"
		platform = "-"
	}

	if player == "-" {
		return player, platform
	}

	switch {
	case strings.Contains(ua, "windows"):
		platform = "Windows"
	case strings.Contains(ua, "android"):
		platform = "Android"
	case strings.Contains(ua, "iphone"), strings.Contains(ua, "ipad"), strings.Contains(ua, "ios"):
		platform = "iOS"
	case strings.Contains(ua, "mac os"), strings.Contains(ua, "macintosh"):
		platform = "macOS"
	case strings.Contains(ua, "linux"):
		platform = "Linux"
	}

	return player, platform
}
