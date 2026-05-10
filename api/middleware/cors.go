package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func CORS(allowOrigins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		allowed := "*"
		if len(allowOrigins) > 0 && allowOrigins[0] != "*" {
			for _, o := range allowOrigins {
				if o == origin {
					allowed = origin
					break
				}
			}
			if allowed == "*" && origin != "" {
				allowed = origin
			}
		}
		if allowed != "" {
			c.Header("Access-Control-Allow-Origin", allowed)
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		// Echo browser preflight so third-party players (PowerPlayer, hls.js) can add custom headers.
		if reqHdrs := strings.TrimSpace(c.GetHeader("Access-Control-Request-Headers")); reqHdrs != "" {
			c.Header("Access-Control-Allow-Headers", reqHdrs)
		} else {
			c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, X-Requested-With, X-Session-ID, Range, If-Range, Cache-Control, Pragma")
		}
		c.Header("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
