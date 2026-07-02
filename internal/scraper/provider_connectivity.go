package scraper

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ProviderTestResult is the outcome of a metadata provider connectivity check.
type ProviderTestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

const testSearchKeyword = "Inception"

// CheckProviderConnectivity verifies API credentials or network reachability for a provider.
func CheckProviderConnectivity(name string, keys map[string]string) ProviderTestResult {
	name = strings.ToLower(strings.TrimSpace(name))
	if keys == nil {
		keys = map[string]string{}
	}
	switch name {
	case "tmdb":
		return testTMDB(keys["tmdb"])
	case "omdb":
		return testOMDb(keys["omdb"])
	case "bangumi":
		return testBangumi(keys["bangumi"])
	case "tvdb":
		return testTVDB(keys["tvdb"])
	case "douban":
		return testDouban()
	case "fanart":
		return testFanart(keys["fanart"])
	case "ai":
		return ProviderTestResult{OK: false, Message: "请在「AI 提供商」页面配置并测试"}
	default:
		return ProviderTestResult{OK: false, Message: fmt.Sprintf("不支持的提供者: %s", name)}
	}
}

func testTMDB(apiKey string) ProviderTestResult {
	if strings.TrimSpace(apiKey) == "" {
		return ProviderTestResult{OK: false, Message: "API Key 未设置"}
	}
	u := "https://api.themoviedb.org/3/configuration?api_key=" + url.QueryEscape(apiKey)
	body, err := httpGetJSON(u, map[string]string{"Accept": "application/json"})
	if err != nil {
		return providerTestFromErr(err)
	}
	var resp struct {
		Images struct {
			BaseURL string `json:"base_url"`
		} `json:"images"`
	}
	if json.Unmarshal(body, &resp) != nil || resp.Images.BaseURL == "" {
		return ProviderTestResult{OK: false, Message: "响应无效，请检查 API Key"}
	}
	return ProviderTestResult{OK: true, Message: "连接成功"}
}

func testOMDb(apiKey string) ProviderTestResult {
	if strings.TrimSpace(apiKey) == "" {
		return ProviderTestResult{OK: false, Message: "API Key 未设置"}
	}
	u := "https://www.omdbapi.com/?apikey=" + url.QueryEscape(apiKey) + "&i=tt1375666"
	body, err := httpGetJSON(u, nil)
	if err != nil {
		return providerTestFromErr(err)
	}
	var resp struct {
		Response string `json:"Response"`
		Error    string `json:"Error"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return ProviderTestResult{OK: false, Message: "响应无效，请检查 API Key"}
	}
	if strings.EqualFold(resp.Response, "True") {
		return ProviderTestResult{OK: true, Message: "连接成功"}
	}
	errMsg := strings.TrimSpace(resp.Error)
	if strings.Contains(strings.ToLower(errMsg), "invalid api key") {
		return ProviderTestResult{OK: false, Message: "API Key 无效"}
	}
	if errMsg != "" {
		return ProviderTestResult{OK: false, Message: errMsg}
	}
	return ProviderTestResult{OK: false, Message: "连接失败"}
}

func testBangumi(accessToken string) ProviderTestResult {
	headers := map[string]string{"User-Agent": "knox-media/1.0"}
	token := strings.TrimSpace(accessToken)
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	u := "https://api.bgm.tv/search/subject/" + url.PathEscape(testSearchKeyword) + "?type=2&responseGroup=small&max_results=1&start=0"
	_, err := httpGetJSON(u, headers)
	if err != nil {
		if token != "" {
			return providerTestFromErr(err)
		}
		return ProviderTestResult{OK: false, Message: "无法连接 Bangumi API"}
	}
	if token != "" {
		return ProviderTestResult{OK: true, Message: "连接成功（Access Token 有效）"}
	}
	return ProviderTestResult{OK: true, Message: "连接成功（无需 API Key）"}
}

func testTVDB(keyRaw string) ProviderTestResult {
	keyRaw = strings.TrimSpace(keyRaw)
	if keyRaw == "" {
		return ProviderTestResult{OK: false, Message: "API Key 未设置（格式: apikey 或 apikey:pin）"}
	}
	apiKey := keyRaw
	pin := ""
	if strings.Contains(keyRaw, ":") {
		parts := strings.SplitN(keyRaw, ":", 2)
		apiKey = strings.TrimSpace(parts[0])
		pin = strings.TrimSpace(parts[1])
	}
	bodyReq := map[string]string{"apikey": apiKey}
	if pin != "" {
		bodyReq["pin"] = pin
	}
	js, _ := json.Marshal(bodyReq)
	req, err := http.NewRequest(http.MethodPost, "https://api4.thetvdb.com/v4/login", strings.NewReader(string(js)))
	if err != nil {
		return ProviderTestResult{OK: false, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := onlineHTTP.Do(req)
	if err != nil {
		return providerTestFromErr(err)
	}
	defer resp.Body.Close()
	loginBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ProviderTestResult{OK: false, Message: "API Key 或 PIN 无效"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ProviderTestResult{OK: false, Message: fmt.Sprintf("登录失败 (HTTP %d)", resp.StatusCode)}
	}
	var login struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if json.Unmarshal(loginBody, &login) != nil || login.Data.Token == "" {
		return ProviderTestResult{OK: false, Message: "未获取到访问令牌"}
	}
	return ProviderTestResult{OK: true, Message: "连接成功"}
}

func testDouban() ProviderTestResult {
	u := "https://movie.douban.com/j/subject_suggest?q=" + url.QueryEscape(testSearchKeyword)
	_, err := httpGetJSON(u, map[string]string{
		"User-Agent": "Mozilla/5.0",
		"Referer":    "https://movie.douban.com/",
	})
	if err != nil {
		return ProviderTestResult{OK: false, Message: "无法连接豆瓣 API"}
	}
	return ProviderTestResult{OK: true, Message: "连接成功（无需 API Key）"}
}

func testFanart(apiKey string) ProviderTestResult {
	if strings.TrimSpace(apiKey) == "" {
		return ProviderTestResult{OK: false, Message: "API Key 未设置"}
	}
	u := "https://webservice.fanart.tv/v3/movies/27205"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return ProviderTestResult{OK: false, Message: err.Error()}
	}
	req.Header.Set("api-key", apiKey)
	resp, err := onlineHTTP.Do(req)
	if err != nil {
		return providerTestFromErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ProviderTestResult{OK: false, Message: "API Key 无效"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ProviderTestResult{OK: false, Message: fmt.Sprintf("请求失败 (HTTP %d)", resp.StatusCode)}
	}
	return ProviderTestResult{OK: true, Message: "连接成功"}
}

func providerTestFromErr(err error) ProviderTestResult {
	if err == nil {
		return ProviderTestResult{OK: true, Message: "连接成功"}
	}
	msg := strings.TrimSpace(err.Error())
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "key missing") || strings.Contains(lower, "api key missing"):
		return ProviderTestResult{OK: false, Message: "API Key 未设置"}
	case strings.Contains(lower, "http 401") || strings.Contains(lower, "http 403"):
		return ProviderTestResult{OK: false, Message: "认证失败，请检查 API Key"}
	case strings.Contains(lower, "http 429"):
		return ProviderTestResult{OK: false, Message: "请求过于频繁，请稍后重试"}
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "dial tcp") || strings.Contains(lower, "no such host") || strings.Contains(lower, "connection refused"):
		return ProviderTestResult{OK: false, Message: "网络连接失败"}
	default:
		return ProviderTestResult{OK: false, Message: msg}
	}
}
