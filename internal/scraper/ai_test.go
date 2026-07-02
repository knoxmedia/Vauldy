package scraper

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScrapeAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{
					"content": `{"title":"老炮儿","overview":"讲述老北京炮儿的故事。","release_date":"2015-12-24","rating":7.8,"genres":["剧情"],"media_type":"movie","year":2015}`,
				},
			}},
		})
	}))
	defer srv.Close()

	old := aiScrapeHTTP
	aiScrapeHTTP = srv.Client()
	t.Cleanup(func() { aiScrapeHTTP = old })

	res, err := scrapeAI("老炮儿", "", 2015, []AIProviderConfig{{
		ID: "test", APIURL: srv.URL, APIKey: "k", Model: "gpt-test",
	}}, "老炮儿HD中英双字")
	if err != nil {
		t.Fatalf("scrapeAI: %v", err)
	}
	if res.Title != "老炮儿" || res.Overview == "" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Source != "ai" {
		t.Fatalf("source=%q", res.Source)
	}
}

func TestScrapeOnlineAIOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{
					"content": `{"title":"爱有来生","overview":"爱情奇幻片。","release_date":"2009-09-01","rating":7.0,"genres":["爱情"],"media_type":"movie","year":2009}`,
				},
			}},
		})
	}))
	defer srv.Close()

	old := aiScrapeHTTP
	aiScrapeHTTP = srv.Client()
	t.Cleanup(func() { aiScrapeHTTP = old })

	cfg := Config{
		Providers: []string{"ai"},
		AIProviders: []AIProviderConfig{{
			ID: "test", APIURL: srv.URL, APIKey: "k", Model: "m",
		}},
	}
	res, err := ScrapeOnline("爱有来生", "ai", cfg)
	if err != nil {
		t.Fatalf("ScrapeOnline: %v", err)
	}
	if !HasMeaningfulScrapeData(res) {
		t.Fatalf("not meaningful: %+v", res)
	}
	if res.Title != "爱有来生" {
		t.Fatalf("title=%q", res.Title)
	}
}
