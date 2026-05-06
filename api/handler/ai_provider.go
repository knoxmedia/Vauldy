package handler

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type aiProviderRow struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	ApiURL       string     `json:"api_url"`
	ApiKey       string     `json:"api_key"`
	Model        string     `json:"model"`
	Enabled      int        `json:"enabled"`
	RequestCount int        `json:"request_count"`
	TokenCount   int        `json:"token_count"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	UpdatedAt    *time.Time `json:"updated_at"`
}

type aiProviderSaveBody struct {
	ApiURL  *string `json:"api_url"`
	ApiKey  *string `json:"api_key"`
	Model   *string `json:"model"`
	Enabled *int    `json:"enabled"`
}

func (h *Handler) ListAIProviders(c *gin.Context) {
	rows, err := h.App.DB.Query(
		`SELECT id, name, api_url, api_key, model, enabled, request_count, token_count, last_used_at, updated_at
		 FROM ai_provider_config ORDER BY id`,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	items := make([]aiProviderRow, 0)
	for rows.Next() {
		var item aiProviderRow
		if err := rows.Scan(&item.ID, &item.Name, &item.ApiURL, &item.ApiKey, &item.Model,
			&item.Enabled, &item.RequestCount, &item.TokenCount, &item.LastUsedAt, &item.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) SaveAIProvider(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id required"})
		return
	}

	var body aiProviderSaveBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Build update dynamically based on non-nil fields.
	setClauses := ""
	args := make([]interface{}, 0)

	if body.ApiURL != nil {
		setClauses += "api_url = ?, "
		args = append(args, *body.ApiURL)
	}
	if body.ApiKey != nil {
		setClauses += "api_key = ?, "
		args = append(args, *body.ApiKey)
	}
	if body.Model != nil {
		setClauses += "model = ?, "
		args = append(args, *body.Model)
	}
	if body.Enabled != nil {
		setClauses += "enabled = ?, "
		args = append(args, *body.Enabled)
	}

	if len(setClauses) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	setClauses += "updated_at = CURRENT_TIMESTAMP"
	args = append(args, id)

	_, err := h.App.DB.Exec(
		`UPDATE ai_provider_config SET `+setClauses+` WHERE id = ?`,
		args...,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}
