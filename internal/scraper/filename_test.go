package scraper

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMediaFilenameChinese(t *testing.T) {
	cases := []struct {
		raw         string
		wantTitle   string
		wantAlt     string
		wantYear    int
	}{
		{"老炮儿HD中英双字", "老炮儿", "", 0},
		{"老炮儿.HD.中英双字", "老炮儿", "", 0},
		{"老炮儿.2015.HD.中英双字.mkv", "老炮儿", "", 2015},
		{"[yyh3d.com]采花和尚.Satyr Monks.1994.LD_D9.x264.mkv", "采花和尚", "Satyr Monks", 1994},
		{"01届.《翼》-《Wings》-1927-1929.mkv", "翼", "Wings", 1927},
		{"蜡笔小新：灼热的春日部舞者.2024.1080p.mkv", "蜡笔小新: 灼热的春日部舞者", "", 2024},
		{"[电影天堂www.dytt8899.com]爱有来生-2009_HD国语中字.mp4", "爱有来生", "", 2009},
		{"我的姐姐 HD国语中字", "我的姐姐", "", 0},
		{"厕所英雄hd国印双语.mkv", "厕所英雄", "", 0},
		{"厕所英雄hd国英双语.mkv", "厕所英雄", "", 0},
		{"碟中谍6：全面瓦解bd中英双字.mp4", "碟中谍6: 全面瓦解", "", 0},
		{"碟中谍6:全面瓦解.BD.中英双字.2018.mp4", "碟中谍6:全面瓦解", "", 2018},
	}
	for _, tc := range cases {
		got := ParseMediaFilename(tc.raw)
		if got.Title != tc.wantTitle || got.TitleAlt != tc.wantAlt || got.Year != tc.wantYear {
			t.Fatalf("%q => %+v want title=%q alt=%q year=%d", tc.raw, got, tc.wantTitle, tc.wantAlt, tc.wantYear)
		}
	}
}

func TestParseMediaFilenameDyttSiteTag(t *testing.T) {
	raw := "[电影天堂www.dytt8899.com]爱有来生-2009_HD国语中字.mp4"
	got := ParseMediaFilename(raw)
	if got.Title != "爱有来生" {
		t.Fatalf("title=%q year=%d want 爱有来生 2009", got.Title, got.Year)
	}
	if got.Year != 2009 {
		t.Fatalf("year=%d want 2009", got.Year)
	}
	if NormalizeTitle(raw) != "爱有来生" {
		t.Fatalf("NormalizeTitle=%q", NormalizeTitle(raw))
	}
}

func TestParseMediaFilenameSiteAtPrefix(t *testing.T) {
	raw := "489155.com@【ai增强】午夜寻花经典作之奶茶妹第7部0306第二场强制高潮喷不停依然内射可真行听译字幕4k增强版.mp4"
	want := "午夜寻花经典作之奶茶妹第7部0306第二场强制高潮喷不停依然内射可真行听译字幕4k增强版"
	got := ParseMediaFilename(raw)
	if got.Title != want {
		t.Fatalf("title=%q want %q", got.Title, want)
	}
	if NormalizeTitle(strings.TrimSuffix(raw, ".mp4")) != want {
		t.Fatalf("NormalizeTitle=%q want %q", NormalizeTitle(strings.TrimSuffix(raw, ".mp4")), want)
	}
}

func TestExtractSearchTermsUsesFilenameParser(t *testing.T) {
	k, alt, y := ExtractSearchTerms("老炮儿HD中英双字")
	if k != "老炮儿" || alt != "" || y != 0 {
		t.Fatalf("got %q alt=%q year=%d", k, alt, y)
	}
}

func TestNormalizeSearchInputUserExamples(t *testing.T) {
	cases := []struct {
		raw       string
		wantTitle string
		wantYear  int
	}{
		{"我的姐姐 HD国语中字", "我的姐姐", 0},
		{"厕所英雄hd国印双语.mkv", "厕所英雄", 0},
		{"碟中谍6：全面瓦解bd中英双字.mp4", "碟中谍6: 全面瓦解", 0},
	}
	for _, tc := range cases {
		title, year := NormalizeSearchInput(tc.raw)
		if title != tc.wantTitle || year != tc.wantYear {
			t.Fatalf("%q => title=%q year=%d want title=%q year=%d", tc.raw, title, year, tc.wantTitle, tc.wantYear)
		}
	}
}

func TestParseMediaFilenameChineseDateTitle(t *testing.T) {
	cases := []struct {
		raw       string
		wantTitle string
	}{
		{"6月27日.mp4", "6月27日"},
		{"6月27日", "6月27日"},
		{"12月25日.mp4", "12月25日"},
		{"1月1日.mp4", "1月1日"},
		{"6月27日公司活动.mp4", "6月27日公司活动"},
	}
	for _, tc := range cases {
		got := ParseMediaFilename(tc.raw)
		if got.Title != tc.wantTitle {
			t.Fatalf("ParseMediaFilename(%q).Title = %q want %q", tc.raw, got.Title, tc.wantTitle)
		}
		if NormalizeTitle(strings.TrimSuffix(tc.raw, filepath.Ext(tc.raw))) != tc.wantTitle {
			t.Fatalf("NormalizeTitle(%q) = %q want %q",
				strings.TrimSuffix(tc.raw, filepath.Ext(tc.raw)),
				NormalizeTitle(strings.TrimSuffix(tc.raw, filepath.Ext(tc.raw))),
				tc.wantTitle)
		}
	}
}
