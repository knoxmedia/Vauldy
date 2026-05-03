package transcode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteVariantToPowerDRMKeyTag(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	variant := filepath.Join(base, "720p.m3u8")
	original := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\n720p_000.m4s\n"
	if err := os.WriteFile(variant, []byte(original), 0o644); err != nil {
		t.Fatalf("write variant: %v", err)
	}

	if err := rewriteVariantToPowerDRM(variant, "417406216575128590"); err != nil {
		t.Fatalf("rewrite variant: %v", err)
	}

	raw, err := os.ReadFile(variant)
	if err != nil {
		t.Fatalf("read variant: %v", err)
	}
	txt := string(raw)
	expect := `#EXT-X-KEY:METHOD=AES-128,URI="skd://417406216575128590",KEYFORMAT="powerdrm",IV=0x00000000000000000000000000000000`
	if !strings.Contains(txt, expect) {
		t.Fatalf("expected powerdrm key tag, got: %s", txt)
	}
}

func TestRewriteVariantToPowerDRMReplacesExistingKeyTag(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	variant := filepath.Join(base, "360p.m3u8")
	original := "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES-CTR,URI=\"data:text/plain;base64,AAA\",KEYID=0xABC\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\n360p_000.m4s\n"
	if err := os.WriteFile(variant, []byte(original), 0o644); err != nil {
		t.Fatalf("write variant: %v", err)
	}

	if err := rewriteVariantToPowerDRM(variant, "kid-test"); err != nil {
		t.Fatalf("rewrite variant: %v", err)
	}

	raw, err := os.ReadFile(variant)
	if err != nil {
		t.Fatalf("read variant: %v", err)
	}
	txt := string(raw)
	if strings.Contains(txt, "SAMPLE-AES-CTR") {
		t.Fatalf("expected old key tag removed, got: %s", txt)
	}
	expect := `#EXT-X-KEY:METHOD=AES-128,URI="skd://kid-test",KEYFORMAT="powerdrm",IV=0x00000000000000000000000000000000`
	if !strings.Contains(txt, expect) {
		t.Fatalf("expected rewritten powerdrm key tag, got: %s", txt)
	}
}
