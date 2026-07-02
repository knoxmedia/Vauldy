package scraper

import "testing"

func TestExtractSearchChineseRelease(t *testing.T) {
	cases := []struct {
		raw      string
		wantKey  string
		wantYear int
	}{
		{"老炮儿HD中英双字", "老炮儿", 0},
		{"老炮儿.HD.中英双字", "老炮儿", 0},
		{"老炮儿.2015.HD.中英双字", "老炮儿", 2015},
		{"[老炮儿].HD.1080p.BluRay.x264", "老炮儿", 0},
		{"让子弹飞.2010.1080p.BluRay.x264", "让子弹飞", 2010},
		{"Inception.2010.1080p.BluRay.x264", "Inception", 2010},
		{"哈利波特与魔法石.Harry.Potter.2001.mkv", "哈利波特与魔法石", 2001},
		{"流浪地球2.2023.2160p.HDR10", "流浪地球2", 2023},
		{"[CHD]老炮儿.2015.1080p.BluRay.x264", "老炮儿", 2015},
	}
	for _, tc := range cases {
		k, y := ExtractSearch(tc.raw)
		if k != tc.wantKey || y != tc.wantYear {
			t.Fatalf("%q => keyword=%q year=%d want %q %d", tc.raw, k, y, tc.wantKey, tc.wantYear)
		}
	}
}

func TestInsertScriptBoundaries(t *testing.T) {
	got := insertScriptBoundaries("老炮儿HD中英双字")
	want := "老炮儿 HD 中英双字"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestOrderProvidersForKeywordChinese(t *testing.T) {
	in := []string{"tmdb", "omdb", "douban", "bangumi", "fanart"}
	got := orderProvidersForKeyword(in, "老炮儿")
	want := []string{"douban", "bangumi", "tmdb", "omdb", "fanart"}
	if len(got) != len(want) {
		t.Fatalf("len %d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d got %q want %q full=%v", i, got[i], want[i], got)
		}
	}
}

func TestOrderProvidersForKeywordEnglishUnchanged(t *testing.T) {
	in := []string{"tmdb", "omdb", "douban"}
	got := orderProvidersForKeyword(in, "Inception")
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("order changed for english title: %v", got)
		}
	}
}
