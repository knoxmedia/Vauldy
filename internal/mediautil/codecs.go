package mediautil

import (
	"encoding/json"
	"strings"
)

// CodecProfile mirrors ffprobe JSON shape used by play routing (container + first video/audio streams).
type CodecProfile struct {
	Container string
	Video     string
	Audio     string
}

// CodecsFromMetaJSON parses media.meta_json from ffprobe output.
func CodecsFromMetaJSON(metaJSON string) CodecProfile {
	out := CodecProfile{}
	if metaJSON == "" {
		return out
	}
	var raw struct {
		Format struct {
			FormatName string `json:"format_name"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal([]byte(metaJSON), &raw); err != nil {
		return out
	}
	out.Container = strings.ToLower(raw.Format.FormatName)
	for _, st := range raw.Streams {
		switch strings.ToLower(st.CodecType) {
		case "video":
			if out.Video == "" {
				out.Video = strings.ToLower(st.CodecName)
			}
		case "audio":
			if out.Audio == "" {
				out.Audio = strings.ToLower(st.CodecName)
			}
		}
	}
	return out
}
