package ffprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
)

func ProbeOptionsIO(ffprobePath string, beforeInput []string, input string, stdin io.Reader) (*Summary, error) {
	return ProbeOptionsContext(context.Background(), ffprobePath, beforeInput, input, stdin)
}
func ProbeOptionsContext(ctx context.Context, ffprobePath string, beforeInput []string, input string, stdin io.Reader) (*Summary, error) {
	args := make([]string, 0, 8+len(beforeInput))
	args = append(args, "-v", "quiet")
	args = append(args, beforeInput...)
	args = append(args, "-print_format", "json", "-show_format", "-show_streams", input)
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	return parseSummaryJSON(out.Bytes())
}
func Output(ffprobePath string, args []string, stdin io.Reader) ([]byte, error) {
	return OutputContext(context.Background(), ffprobePath, args, stdin)
}
func OutputContext(ctx context.Context, ffprobePath string, args []string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	return out, nil
}
func parseSummaryJSON(raw []byte) (*Summary, error) {
	var pr ProbeResult
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, fmt.Errorf("ffprobe json: %w", err)
	}
	s := &Summary{RawJSON: string(raw)}
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
