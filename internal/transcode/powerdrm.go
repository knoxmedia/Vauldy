package transcode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const PowerDRMIV = "0x00000000000000000000000000000000"

func rewriteManifestsToPowerDRM(outDir string, kid string) error {
	kid = strings.TrimSpace(kid)
	if strings.TrimSpace(outDir) == "" || kid == "" {
		return nil
	}
	if err := rewriteMasterForPowerDRM(filepath.Join(outDir, "master.m3u8")); err != nil {
		return err
	}
	pls, err := filepath.Glob(filepath.Join(outDir, "*.m3u8"))
	if err != nil {
		return err
	}
	for _, p := range pls {
		if strings.EqualFold(filepath.Base(p), "master.m3u8") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(filepath.Base(p)), "audio") {
			continue
		}
		if err := rewriteVariantToPowerDRM(p, kid); err != nil {
			return err
		}
	}
	return nil
}

func rewriteMasterForPowerDRM(masterPath string) error {
	raw, err := os.ReadFile(masterPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "#EXT-X-SESSION-KEY:") {
			continue
		}
		out = append(out, ln)
	}
	return os.WriteFile(masterPath, []byte(strings.Join(out, "\n")), 0o644)
}

func rewriteVariantToPowerDRM(playlistPath string, kid string) error {
	raw, err := os.ReadFile(playlistPath)
	if err != nil {
		return err
	}
	tag := fmt.Sprintf(`#EXT-X-KEY:METHOD=AES-128,URI="skd://%s",KEYFORMAT="powerdrm",IV=%s`, kid, PowerDRMIV)
	lines := strings.Split(string(raw), "\n")
	out := make([]string, 0, len(lines)+1)
	inserted := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#EXT-X-KEY:") {
			if !inserted {
				out = append(out, tag)
				inserted = true
			}
			continue
		}
		out = append(out, ln)
		if !inserted && (strings.HasPrefix(t, "#EXT-X-MAP:") || strings.HasPrefix(t, "#EXT-X-TARGETDURATION:")) {
			out = append(out, tag)
			inserted = true
		}
	}
	if !inserted {
		out = append(out, tag)
	}
	return os.WriteFile(playlistPath, []byte(strings.Join(out, "\n")), 0o644)
}
