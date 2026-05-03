package ffprobe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
)

type ProbeResult struct {
	Format  Format   `json:"format"`
	Streams []Stream `json:"streams"`
}

type Format struct {
	Duration   string `json:"duration"`
	BitRate    string `json:"bit_rate"`
	FormatName string `json:"format_name"`
}

type StreamTags struct {
	Language string `json:"language"`
	Title    string `json:"title"`
}

type Stream struct {
	CodecType  string      `json:"codec_type"`
	CodecName  string      `json:"codec_name"`
	Width      int         `json:"width"`
	Height     int         `json:"height"`
	Duration   string      `json:"duration"`
	BitRate    string      `json:"bit_rate"`
	Index      int         `json:"index"`
	Tags       *StreamTags `json:"tags"`
}

type Summary struct {
	DurationSec int
	Width       int
	Height      int
	Bitrate     int
	Format      string
	VideoCodec  string
	AudioCodec  string
	RawJSON     string
}

// Probe runs ffprobe with default analysis depth (reads enough of the file for accurate metadata).
func Probe(ffprobePath, filePath string) (*Summary, error) {
	return ProbeOptions(ffprobePath, filePath, nil)
}

// ScanProbeExtraFast limits analyzeduration/probesize so library scans spend less time per file.
// Disable via config scan.fast_ffprobe: false if duration/codecs are missing on some containers.
func ScanProbeExtraFast() []string {
	return []string{
		"-analyzeduration", "1000000", // 1s (microseconds)
		"-probesize", "16777216", // 16 MiB
	}
}

// ProbeOptions runs ffprobe; beforeInput are inserted after -v quiet (e.g. ScanProbeExtraFast()).
func ProbeOptions(ffprobePath, filePath string, beforeInput []string) (*Summary, error) {
	args := make([]string, 0, 8+len(beforeInput))
	args = append(args, "-v", "quiet")
	args = append(args, beforeInput...)
	args = append(args, "-print_format", "json", "-show_format", "-show_streams", filePath)
	cmd := exec.Command(ffprobePath, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	var pr ProbeResult
	if err := json.Unmarshal(out.Bytes(), &pr); err != nil {
		return nil, fmt.Errorf("ffprobe json: %w", err)
	}
	s := &Summary{RawJSON: out.String()}
	if pr.Format.FormatName != "" {
		s.Format = pr.Format.FormatName
	}
	if pr.Format.BitRate != "" {
		s.Bitrate, _ = strconv.Atoi(pr.Format.BitRate)
	}
	if pr.Format.Duration != "" {
		f, _ := strconv.ParseFloat(pr.Format.Duration, 64)
		s.DurationSec = int(f + 0.5)
	}
	for _, st := range pr.Streams {
		switch st.CodecType {
		case "video":
			if s.Width == 0 {
				s.Width = st.Width
				s.Height = st.Height
				s.VideoCodec = st.CodecName
			}
			if s.DurationSec == 0 && st.Duration != "" {
				f, _ := strconv.ParseFloat(st.Duration, 64)
				s.DurationSec = int(f + 0.5)
			}
		case "audio":
			if s.AudioCodec == "" {
				s.AudioCodec = st.CodecName
			}
		}
	}
	return s, nil
}
