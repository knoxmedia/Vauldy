package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"knox-media/internal/auth"
)

type createAPIClientBody struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

func (h *Handler) ListAPIClients(c *gin.Context) {
	rows, err := h.App.DB.Query(`SELECT id, name, description, client_id, revoked, created_at FROM api_client ORDER BY id DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var id, revoked int
		var name, desc, clientID, created string
		if err := rows.Scan(&id, &name, &desc, &clientID, &revoked, &created); err != nil {
			continue
		}
		items = append(items, gin.H{
			"app_id":      id,
			"name":        name,
			"description": desc,
			"client_id":   clientID,
			"revoked":     revoked == 1,
			"created_at":  created,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) CreateAPIClient(c *gin.Context) {
	var body createAPIClientBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
		return
	}
	desc := strings.TrimSpace(body.Description)

	plainSecret, err := randomHex(32)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plainSecret), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	clientID := "knox_" + randomHexNoErr(16)

	res, err := h.App.DB.Exec(
		`INSERT INTO api_client (name, description, client_id, secret_hash) VALUES (?, ?, ?, ?)`,
		name, desc, clientID, string(hash),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	appID, _ := res.LastInsertId()
	c.JSON(http.StatusCreated, gin.H{
		"app_id":        appID,
		"client_id":     clientID,
		"client_secret": plainSecret,
		"name":          name,
		"description":   desc,
		"hint":          "client_secret 仅本次返回，请妥善保存",
	})
}

func (h *Handler) RevokeAPIClient(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	res, err := h.App.DB.Exec(`UPDATE api_client SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomHexNoErr(nBytes int) string {
	s, err := randomHex(nBytes)
	if err != nil {
		return strings.Repeat("0", nBytes*2)
	}
	return s
}

// OAuthToken implements OAuth2 client_credentials (RFC 6749) using client_id + client_secret, returning a JWT Bearer token.
func (h *Handler) OAuthToken(c *gin.Context) {
	var req struct {
		GrantType    string `form:"grant_type" json:"grant_type"`
		ClientID     string `form:"client_id" json:"client_id"`
		ClientSecret string `form:"client_secret" json:"client_secret"`
	}
	ct := strings.ToLower(c.GetHeader("Content-Type"))
	if strings.Contains(ct, "application/json") {
		_ = c.ShouldBindJSON(&req)
	} else {
		_ = c.ShouldBind(&req)
	}

	if !strings.EqualFold(strings.TrimSpace(req.GrantType), "client_credentials") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_grant_type", "error_description": "grant_type must be client_credentials"})
		return
	}
	cid := strings.TrimSpace(req.ClientID)
	sec := strings.TrimSpace(req.ClientSecret)
	if cid == "" || sec == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "error_description": "client_id and client_secret required"})
		return
	}

	var id int64
	var hash string
	err := h.App.DB.QueryRow(
		`SELECT id, secret_hash FROM api_client WHERE client_id = ? AND COALESCE(revoked,0) = 0`,
		cid,
	).Scan(&id, &hash)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_client"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(sec)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_client"})
		return
	}

	hours := h.App.Config.Security.TokenHours
	if hours <= 0 {
		hours = 168
	}
	token, err := auth.SignClientToken(h.App.Config.Security.JWTSecret, hours, id, cid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   hours * 3600,
	})
}
