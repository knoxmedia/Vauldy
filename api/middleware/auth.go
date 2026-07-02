package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/auth"
	"knox-media/internal/config"
)

const (
	ctxUserIDKey = "user_id"
	ctxRoleKey   = "role"
	ctxUsernameKey = "username"
)

func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// tokenFromRequest returns JWT from Authorization header, or from query access_token when allowQuery is true (for media playback URLs).
func tokenFromRequest(c *gin.Context, allowQuery bool) string {
	t := bearerToken(c)
	if t != "" {
		return t
	}
	if allowQuery {
		if q := strings.TrimSpace(c.Query("access_token")); q != "" {
			return q
		}
	}
	return ""
}

// RequireAuthentication validates JWT and sets user_id, username, role. allowQuery enables ?access_token= for GET play/hls.
func RequireAuthentication(cfg *config.Config, allowQuery bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := tokenFromRequest(c, allowQuery)
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}
		claims, err := auth.ParseToken(cfg.Security.JWTSecret, token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			c.Abort()
			return
		}
		c.Set(ctxUserIDKey, claims.UserID)
		c.Set(ctxUsernameKey, claims.Username)
		c.Set(ctxRoleKey, claims.Role)
		c.Next()
	}
}

// RequireAdmin must run after RequireAuthentication. Only role "admin" may proceed.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !IsAdmin(c) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden", "message": "admin only"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func IsAdmin(c *gin.Context) bool {
	return strings.EqualFold(Role(c), "admin")
}

// IsAPIClient is true for JWTs issued via OAuth2 client_credentials (machine tokens).
func IsAPIClient(c *gin.Context) bool {
	return strings.EqualFold(Role(c), "api_client")
}

func Role(c *gin.Context) string {
	v, ok := c.Get(ctxRoleKey)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func Username(c *gin.Context) string {
	v, ok := c.Get(ctxUsernameKey)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func UserID(c *gin.Context) int64 {
	v, ok := c.Get(ctxUserIDKey)
	if !ok {
		return 0
	}
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	default:
		return 0
	}
}
