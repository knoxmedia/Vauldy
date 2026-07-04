package handler

import (
	"log"

	"knox-media/internal/caststore"
	"knox-media/internal/scraper"
)

// importMediaCreditsAfterScrape fetches TMDB credits and links cast/crew after a successful media scrape.
func (h *Handler) importMediaCreditsAfterScrape(libraryID, mediaID int64, metaJSON, libraryType string) {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 {
		return
	}
	tmdbID, mediaType := extractTMDBIDFromMeta(metaJSON, libraryType)
	if tmdbID == "" {
		return
	}
	cfg := h.readLibraryScrapeConfig(libraryID)
	if cfg.APIKeys["tmdb"] == "" {
		return
	}
	credits, err := scraper.FetchTMDBCredits(tmdbID, mediaType, "zh-CN", cfg.APIKeys["tmdb"])
	if err != nil {
		log.Printf("import credits media=%d tmdb=%s: %v", mediaID, tmdbID, err)
		return
	}
	avatarBase := scraper.GetTMDBImageBase() + "/t/p/w185"
	n, err := caststore.ImportCredits(h.App.DB, mediaID, credits, avatarBase)
	if err != nil {
		log.Printf("import credits media=%d: %v", mediaID, err)
		return
	}
	if n > 0 {
		log.Printf("imported %d cast/crew links for media=%d", n, mediaID)
	}
}
