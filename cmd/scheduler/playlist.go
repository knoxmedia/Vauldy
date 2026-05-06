package scheduler

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	models "knox-media/internal/model"
)

// playlistVariant描述master playlist中的一个码率档位.
type playlistVariant struct {
	Name        string // 例如 "2000k", "passthrough"
	Bandwidth   int    // bps
	Width       int
	Height      int
	Passthrough bool // 视频流复制
}

// builtinLadder mirrors Jellyfin/Emby的多码率阶梯，按高度从大到小排列。
// Bandwidth 单位 bps，与码率近似匹配以方便 ABR 切换。
var builtinLadder = []playlistVariant{
	{Name: "8000k", Bandwidth: 8000000, Width: 3840, Height: 2160},
	{Name: "4000k", Bandwidth: 4000000, Width: 1920, Height: 1080},
	{Name: "2000k", Bandwidth: 2000000, Width: 1280, Height: 720},
	{Name: "1000k", Bandwidth: 1000000, Width: 854, Height: 480},
	{Name: "500k", Bandwidth: 500000, Width: 640, Height: 360},
}

// adaptiveLadder 选取与源高度相符的子阶梯：始终包含一个不超过源高度的最高档（直播/seek 时该档作为目标），
// 同时保留两到三个低档以适应弱网。当源高度未知（=0）时退化为完整阶梯。
func adaptiveLadder(srcHeight int, maxClientHeight int) []playlistVariant {
	srcH := srcHeight
	maxH := maxClientHeight
	if srcH <= 0 {
		srcH = math.MaxInt32
	}
	if maxH <= 0 {
		maxH = math.MaxInt32
	}
	cap := srcH
	if maxH < cap {
		cap = maxH
	}

	out := make([]playlistVariant, 0, len(builtinLadder))
	includedTop := false
	for _, v := range builtinLadder {
		if v.Height > cap {
			continue
		}
		if !includedTop {
			out = append(out, v)
			includedTop = true
			continue
		}
		out = append(out, v)
	}
	if !includedTop {
		// 客户端比最低档还小（极端情况），保留最低档以免空 master。
		out = append(out, builtinLadder[len(builtinLadder)-1])
	}
	return out
}

// targetDuration 返回 EXT-X-TARGETDURATION，按 HLS 规范应当 ≥ 任意分片实际时长（向上取整秒）。
func targetDuration(segs []models.VideoSegmentInfo) int {
	max := 0.0
	for _, s := range segs {
		if s.Duration > max {
			max = s.Duration
		}
	}
	if max <= 0 {
		return 6
	}
	return int(math.Ceil(max))
}

func targetDurationAudio(segs []models.AudioSegmentInfo) int {
	max := 0.0
	for _, s := range segs {
		if s.Duration > max {
			max = s.Duration
		}
	}
	if max <= 0 {
		return 6
	}
	return int(math.Ceil(max))
}

// audioLanguages 收集索引中出现的语言（按出现顺序去重，空字符串映射为 und）。
func audioLanguages(segs []models.AudioSegmentInfo) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for _, s := range segs {
		l := strings.TrimSpace(s.Language)
		if l == "" {
			l = "und"
		}
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// videoCodecsAttr 返回 EXT-X-STREAM-INF 的 CODECS 属性值。
// 对于 passthrough 档位若源不是 H.264 则按 hevc/Main10 推测；否则统一 avc1.640028 + mp4a.40.2。
func videoCodecsAttr(passthrough bool, srcCodec string) string {
	v := strings.ToLower(strings.TrimSpace(srcCodec))
	if passthrough {
		switch v {
		case "hevc", "h265":
			return "hvc1.1.6.L120.90,mp4a.40.2"
		case "vp9":
			return "vp09.00.50.08,mp4a.40.2"
		case "av1":
			return "av01.0.05M.08,mp4a.40.2"
		}
	}
	return "avc1.640028,mp4a.40.2"
}

// formatBitrate 把 "2000k" 转为 bps（用于 STREAM-INF BANDWIDTH 兜底）。
func formatBitrate(name string) int {
	n := strings.ToLower(strings.TrimSpace(name))
	if strings.HasSuffix(n, "k") {
		v, err := strconv.Atoi(n[:len(n)-1])
		if err == nil {
			return v * 1000
		}
	}
	if strings.HasSuffix(n, "m") {
		v, err := strconv.Atoi(n[:len(n)-1])
		if err == nil {
			return v * 1000000
		}
	}
	return 0
}

// renderMediaPlaylist 渲染 #EXTINF + URL 行列表（视频/音频共用模板）。
func renderMediaPlaylist(target int, segments []segmentEntry) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	b.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", target))
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n\n")
	for _, seg := range segments {
		b.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", seg.Duration))
		b.WriteString(seg.URL)
		b.WriteString("\n")
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// segmentEntry 是渲染媒体 playlist 时的一行（duration + url）。
type segmentEntry struct {
	Duration float64
	URL      string
}
