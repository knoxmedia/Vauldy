package ffprobe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
)

// SubtitleStream describes an embedded subtitle track from ffprobe.
type SubtitleStream struct {
	Index     int
	CodecName string
	Language  string
	Title     string
}

// SubtitleStreams returns embedded subtitle stream metadata for the given file.
func SubtitleStreams(ffprobePath, filePath string) ([]SubtitleStream, error) {
	cmd := exec.Command(ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		filePath,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	var pr struct {
		Streams []Stream `json:"streams"`
	}
	if err := json.Unmarshal(out.Bytes(), &pr); err != nil {
		return nil, fmt.Errorf("ffprobe json: %w", err)
	}
	var subs []SubtitleStream
	for _, st := range pr.Streams {
		if st.CodecType != "subtitle" {
			continue
		}
		s := SubtitleStream{Index: st.Index, CodecName: st.CodecName}
		if st.Tags != nil {
			s.Language = st.Tags.Language
			s.Title = st.Tags.Title
		}
		subs = append(subs, s)
	}
	return subs, nil
}
