package subtitle

import "strings"

// Bitmap subtitle codecs: PGS (Blu-ray), VobSub (DVD), etc. — not directly convertible to WebVTT without OCR.
func IsBitmapSubtitleCodec(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "hdmv_pgs_subtitle", "pgssub", "pgs",
		"dvd_subtitle", "dvdsub", "vobsub",
		"xsub", "dvb_subtitle", "dvb_teletext":
		return true
	default:
		return false
	}
}

// Text-based codecs that ffmpeg can usually mux to WebVTT.
func IsTextSubtitleCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	switch c {
	case "subrip", "srt", "ass", "ssa", "webvtt", "mov_text",
		"stl", "microdvd", "text", "subviewer", "jacosub", "realtext",
		"sami", "eia_608", "eia_708":
		return true
	default:
		return false
	}
}
