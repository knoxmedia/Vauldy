package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/auth"
	"knox-media/internal/config"
)

func TestRequireAuthenticationAcceptsAccessTokenQueryWhenEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "test-jwt-secret"
	token, err := auth.SignToken(secret, 1, 1, "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Security.JWTSecret = secret

	r := gin.New()
	r.GET("/artwork", RequireAuthentication(cfg, true), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/artwork?access_token="+token, nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("allowQuery=true: status=%d body=%s", w.Code, w.Body.String())
	}

	r2 := gin.New()
	r2.GET("/artwork", RequireAuthentication(cfg, false), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/artwork?access_token="+token, nil)
	r2.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("allowQuery=false: status=%d body=%s", w2.Code, w2.Body.String())
	}
}
