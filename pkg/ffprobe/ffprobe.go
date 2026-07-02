package ffprobe

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
	return ProbeOptionsIO(ffprobePath, beforeInput, filePath, nil)
}
