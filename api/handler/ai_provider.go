package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/scraper"
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

func (h *Handler) TestAIProvider(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id required"})
		return
	}
	c.JSON(http.StatusOK, h.testSingleAIProvider(id))
}

func (h *Handler) testSingleAIProvider(id string) scraper.ProviderTestResult {
	var name, apiURL, apiKey, model string
	var enabled int
	err := h.App.DB.QueryRow(
		`SELECT name, api_url, api_key, model, enabled FROM ai_provider_config WHERE id = ?`,
		id,
	).Scan(&name, &apiURL, &apiKey, &model, &enabled)
	if err == sql.ErrNoRows {
		return scraper.ProviderTestResult{OK: false, Message: "提供商不存在"}
	}
	if err != nil {
		return scraper.ProviderTestResult{OK: false, Message: "无法读取配置"}
	}
	if strings.TrimSpace(apiURL) == "" {
		return scraper.ProviderTestResult{OK: false, Message: "API 地址未设置"}
	}
	if strings.TrimSpace(apiKey) == "" && !isLocalAIURL(apiURL) {
		return scraper.ProviderTestResult{OK: false, Message: "API Key 未设置"}
	}
	if strings.TrimSpace(model) == "" && !isLocalAIURL(apiURL) {
		return scraper.ProviderTestResult{OK: false, Message: "模型未设置"}
	}
	if err := pingOpenAICompatible(apiURL, apiKey); err != nil {
		return scraper.ProviderTestResult{OK: false, Message: err.Error()}
	}
	msg := "连接成功"
	if enabled != 1 {
		msg += "（当前为停用状态）"
	}
	return scraper.ProviderTestResult{OK: true, Message: msg}
}

func isLocalAIURL(apiURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(apiURL))
	return strings.Contains(lower, "localhost") || strings.Contains(lower, "127.0.0.1")
}

func pingOpenAICompatible(apiURL, apiKey string) error {
	base := strings.TrimRight(strings.TrimSpace(apiURL), "/")
	u := base + "/models"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("网络连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("API Key 无效")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		req2, _ := http.NewRequest(http.MethodGet, base+"/api/tags", nil)
		resp2, err2 := client.Do(req2)
		if err2 == nil {
			defer resp2.Body.Close()
			if resp2.StatusCode >= 200 && resp2.StatusCode < 300 {
				return nil
			}
		}
	}
	return fmt.Errorf("请求失败 (HTTP %d)", resp.StatusCode)
}
