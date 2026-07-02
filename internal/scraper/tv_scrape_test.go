package scraper

import "testing"

func TestParseTVScrapeContext(t *testing.T) {
	t.Parallel()
	raw := `{"tv":{"series_title":"剧集A","season":1,"episode":2,"year":2020,"tmdb_id":"1429"}}`
	ctx := ParseTVScrapeContext(raw)
	if !ctx.ValidEpisode() {
		t.Fatal("expected valid")
	}
	if ctx.SeriesTitle != "剧集A" || ctx.Season != 1 || ctx.Episode != 2 || ctx.Year != 2020 || ctx.TMDBID != "1429" {
		t.Fatalf("unexpected ctx: %+v", ctx)
	}
}

func TestFormatEpisodeDisplayTitle(t *testing.T) {
	t.Parallel()
	got := formatEpisodeDisplayTitle("Show", 1, 3, "Pilot")
	want := "Show - S01E03 - Pilot"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTVScrapeProviderOrder_Anime(t *testing.T) {
	t.Parallel()
	order := tvScrapeProviderOrder([]string{"tmdb", "omdb", "bangumi", "tvdb"}, "anime")
	if order[0] != "bangumi" {
		t.Fatalf("anime first provider=%q want bangumi", order[0])
	}
}
