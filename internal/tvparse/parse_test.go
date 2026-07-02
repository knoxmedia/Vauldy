package tvparse

import (
	"path/filepath"
	"testing"
)

func TestParseVideoPath_StandardLayout(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash("/media/TV/剧集A/Season 01/剧集A - S01E01.mp4")
	info, ok := ParseVideoPath(root)
	if !ok {
		t.Fatal("expected ok")
	}
	if info.SeriesTitle != "剧集A" {
		t.Fatalf("series=%q want 剧集A", info.SeriesTitle)
	}
	if info.SeasonNum != 1 || info.EpisodeNum != 1 {
		t.Fatalf("S/E=%d/%d want 1/1", info.SeasonNum, info.EpisodeNum)
	}
}

func TestParseVideoPath_YearInFolder(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash("/media/TV/剧集C (2020)/Season 2/剧集C - S02E03.mkv")
	info, ok := ParseVideoPath(root)
	if !ok {
		t.Fatal("expected ok")
	}
	if info.Year != 2020 {
		t.Fatalf("year=%d want 2020", info.Year)
	}
	if info.SeasonNum != 2 || info.EpisodeNum != 3 {
		t.Fatalf("S/E=%d/%d want 2/3", info.SeasonNum, info.EpisodeNum)
	}
}

func TestParseVideoPath_CrossFolderSameSeries(t *testing.T) {
	t.Parallel()
	a := filepath.FromSlash("/media/TV/剧集A (2019)/Season 01/Show - S01E01.mp4")
	b := filepath.FromSlash("/media/anime/剧集A (2021)/Season 01/Show - S01E02.mp4")
	ia, _ := ParseVideoPath(a)
	ib, _ := ParseVideoPath(b)
	if ia.SeriesTitleNorm != ib.SeriesTitleNorm {
		t.Fatalf("norm mismatch: %q vs %q", ia.SeriesTitleNorm, ib.SeriesTitleNorm)
	}
}

func TestParseVideoPath_TMDBID(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash("/media/4K/Attack on Titan [tmdbid=1429]/Season 1/AoT - S01E01.mp4")
	info, ok := ParseVideoPath(root)
	if !ok {
		t.Fatal("expected ok")
	}
	if info.TMDBID != "1429" {
		t.Fatalf("tmdb=%q want 1429", info.TMDBID)
	}
}

func TestParseVideoPath_NoEpisode(t *testing.T) {
	t.Parallel()
	_, ok := ParseVideoPath(filepath.FromSlash("/media/Movies/Film.2024.mp4"))
	if ok {
		t.Fatal("expected not ok for movie file")
	}
}

func TestNormalizeSeriesTitle(t *testing.T) {
	t.Parallel()
	a := NormalizeSeriesTitle("攻壳机动队 SAC 1080p")
	b := NormalizeSeriesTitle("攻壳机动队 SAC")
	if a != b {
		t.Fatalf("%q != %q", a, b)
	}
}

func TestParseVideoPath_Specials(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash("/media/TV/My Show/Specials/My Show - S00E01.mkv")
	info, ok := ParseVideoPath(root)
	if !ok {
		t.Fatal("expected ok")
	}
	if !info.IsSpecial || info.SeasonNum != 0 {
		t.Fatalf("special season=%d isSpecial=%v", info.SeasonNum, info.IsSpecial)
	}
}

func TestParseVideoPath_ChineseFlatFolder(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash(`K:/movies/电视剧/去有风的地方/去有风的地方第1集.mp4`)
	info, ok := ParseVideoPath(root)
	if !ok {
		t.Fatal("expected ok")
	}
	if info.SeriesTitle != "去有风的地方" {
		t.Fatalf("series=%q want 去有风的地方", info.SeriesTitle)
	}
	if info.SeasonNum != 1 || info.EpisodeNum != 1 {
		t.Fatalf("S/E=%d/%d want 1/1", info.SeasonNum, info.EpisodeNum)
	}
}

func TestParseVideoPath_ChineseSeasonInFolder(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash(`K:/movies/电视剧/奇思妙探第三季/奇思妙探第三季第5集.mp4`)
	info, ok := ParseVideoPath(root)
	if !ok {
		t.Fatal("expected ok")
	}
	if info.SeriesTitle != "奇思妙探" {
		t.Fatalf("series=%q want 奇思妙探", info.SeriesTitle)
	}
	if info.SeasonNum != 3 || info.EpisodeNum != 5 {
		t.Fatalf("S/E=%d/%d want 3/5", info.SeasonNum, info.EpisodeNum)
	}
}

func TestParseVideoPath_ChineseNumericOnly(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash(`K:/movies/电视剧/宿醉/01.mp4`)
	info, ok := ParseVideoPath(root)
	if !ok {
		t.Fatal("expected ok")
	}
	if info.SeriesTitle != "宿醉" {
		t.Fatalf("series=%q want 宿醉", info.SeriesTitle)
	}
	if info.EpisodeNum != 1 {
		t.Fatalf("episode=%d want 1", info.EpisodeNum)
	}
}

func TestParseVideoPath_SameFolderSameSeries(t *testing.T) {
	t.Parallel()
	a := filepath.FromSlash(`K:/movies/电视剧/去有风的地方/去有风的地方第1集.mp4`)
	b := filepath.FromSlash(`K:/movies/电视剧/去有风的地方/去有风的地方第23集.mp4`)
	ia, ok1 := ParseVideoPath(a)
	ib, ok2 := ParseVideoPath(b)
	if !ok1 || !ok2 {
		t.Fatalf("parse failed ok1=%v ok2=%v", ok1, ok2)
	}
	if ia.SeriesTitleNorm != ib.SeriesTitleNorm {
		t.Fatalf("norm mismatch %q vs %q", ia.SeriesTitleNorm, ib.SeriesTitleNorm)
	}
	if ia.SourceFolder != ib.SourceFolder {
		t.Fatalf("source folder should match for same show directory")
	}
}

func TestFormatEpisodeLabel_Chinese(t *testing.T) {
	t.Parallel()
	label := FormatEpisodeLabel(EpisodeInfo{SeriesTitle: "去有风的地方", SeasonNum: 1, EpisodeNum: 2})
	if label != "去有风的地方 - 第2集" {
		t.Fatalf("label=%q", label)
	}
}

func TestParseVideoPath_ChineseWithSpace(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash(`K:/movies/电视剧/去有风的地方/去有风的地方 第3集.mp4`)
	info, ok := ParseVideoPath(root)
	if !ok {
		t.Fatal("expected ok")
	}
	if info.EpisodeNum != 3 {
		t.Fatalf("episode=%d want 3", info.EpisodeNum)
	}
}

func TestParseVideoPath_EpisodeOnlyFilename(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash(`K:/movies/电视剧/去有风的地方/第40集.mp4`)
	info, ok := ParseVideoPath(root)
	if !ok {
		t.Fatal("expected ok")
	}
	if info.SeriesTitle != "去有风的地方" || info.EpisodeNum != 40 {
		t.Fatalf("got %+v", info)
	}
}

func TestParseVideoPath_CJKAdjacentSxxEyy(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash(`K:/movies/电视剧/奇思妙探第三季/奇思妙探S03E05.mp4`)
	info, ok := ParseVideoPath(root)
	if !ok {
		t.Fatal("expected ok")
	}
	if info.SeriesTitle != "奇思妙探" || info.SeasonNum != 3 || info.EpisodeNum != 5 {
		t.Fatalf("got %+v", info)
	}
}

func TestParseVideoPath_TrailingNumber(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash(`K:/movies/电视剧/宿醉/宿醉2.mp4`)
	info, ok := ParseVideoPath(root)
	if !ok {
		t.Fatal("expected ok")
	}
	if info.SeriesTitle != "宿醉" || info.EpisodeNum != 2 {
		t.Fatalf("got %+v", info)
	}
}

func TestParseStoredTVMeta(t *testing.T) {
	t.Parallel()
	raw := `{"tv":{"series_title":"剧集A","season":1,"episode":2,"year":2020}}`
	info, ok := ParseStoredTVMeta(raw)
	if !ok || info.SeriesTitle != "剧集A" || info.EpisodeNum != 2 {
		t.Fatalf("unexpected: ok=%v info=%+v", ok, info)
	}
}
