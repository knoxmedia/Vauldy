package pretranscode

import (
	"database/sql"
	"testing"

	"knox-media/internal/jit/hwenc"
	"knox-media/internal/store"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestListPresetsSeededWithBuiltin(t *testing.T) {
	db := newTestDB(t)
	svc := &PresetService{DB: db}
	presets, err := svc.ListPresets()
	if err != nil {
		t.Fatal(err)
	}
	if len(presets) < 7 {
		t.Errorf("expected ≥7 builtin presets, got %d", len(presets))
	}
	// HLS-标准 must be builtin and enabled.
	var found bool
	for _, p := range presets {
		if p.Name == "HLS-标准" && p.IsBuiltin && p.IsEnabled {
			found = true
		}
	}
	if !found {
		t.Errorf("HLS-标准 builtin missing")
	}
}

func TestCreatePresetWithRenditions(t *testing.T) {
	db := newTestDB(t)
	svc := &PresetService{DB: db}
	p, err := svc.CreatePreset(CreatePresetInput{
		Name: "test-hls", OutputFormat: "hls", VideoCodec: "libx264", VideoPreset: "veryfast", VideoCRF: 23,
		AudioCodec: "aac", AudioBitrate: "128k", AudioChannels: 2, AudioSampleRate: 48000,
		Renditions: []Rendition{
			{Name: "360p", Height: 360, VideoBitrate: "850k"},
			{Name: "720p", Height: 720, VideoBitrate: "2800k"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 || len(p.Renditions) != 2 {
		t.Errorf("preset create failed: %+v", p)
	}
}

func TestDeleteBuiltinPresetRejected(t *testing.T) {
	db := newTestDB(t)
	svc := &PresetService{DB: db}
	presets, _ := svc.ListPresets()
	for _, p := range presets {
		if p.IsBuiltin {
			err := svc.DeletePreset(p.ID)
			if err != ErrBuiltinProtected {
				t.Errorf("deleting builtin should be rejected, got %v", err)
			}
			return
		}
	}
}

func TestClonePreset(t *testing.T) {
	db := newTestDB(t)
	svc := &PresetService{DB: db}
	presets, _ := svc.ListPresets()
	var srcID int64
	for _, p := range presets {
		if p.IsBuiltin {
			srcID = p.ID
			break
		}
	}
	src, err := svc.GetPreset(srcID)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := svc.ClonePreset(srcID, "cloned")
	if err != nil {
		t.Fatal(err)
	}
	if clone.ID == src.ID {
		t.Errorf("clone did not get a new id")
	}
	if clone.IsBuiltin {
		t.Errorf("clone should not be builtin")
	}
	if len(clone.Renditions) != len(src.Renditions) {
		t.Errorf("rendition count mismatch: %d vs %d", len(clone.Renditions), len(src.Renditions))
	}
}

func TestMP4ForcesNoEncryption(t *testing.T) {
	db := newTestDB(t)
	svc := &PresetService{DB: db}
	p, err := svc.CreatePreset(CreatePresetInput{
		Name: "test-mp4", OutputFormat: "mp4", EncryptionMode: "aes128",
		VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.EncryptionMode != "none" {
		t.Errorf("MP4 preset must force encryption_mode=none (SRS ENC-05), got %s", p.EncryptionMode)
	}
}

func TestTogglePreset(t *testing.T) {
	db := newTestDB(t)
	svc := &PresetService{DB: db}
	p, _ := svc.CreatePreset(CreatePresetInput{
		Name: "toggle-test", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}},
	})
	// Preset starts enabled (is_enabled=1). First toggle disables.
	enabled, _ := svc.TogglePreset(p.ID)
	if enabled {
		t.Errorf("first toggle should disable")
	}
	enabled, _ = svc.TogglePreset(p.ID)
	if !enabled {
		t.Errorf("second toggle should re-enable")
	}
}

func TestRenditionSortAndCRUD(t *testing.T) {
	db := newTestDB(t)
	svc := &PresetService{DB: db}
	p, _ := svc.CreatePreset(CreatePresetInput{
		Name: "rend-test", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{
			{Name: "360p", Height: 360, VideoBitrate: "850k"},
			{Name: "720p", Height: 720, VideoBitrate: "2800k"},
		},
	})
	// Add a new rendition at the end.
	r, err := svc.AddRendition(p.ID, Rendition{Name: "1080p", Height: 1080, VideoBitrate: "5000k"})
	if err != nil {
		t.Fatal(err)
	}
	// Reorder: 1080p first.
	if err := svc.SortRenditions(p.ID, []int64{r.ID, p.Renditions[1].ID, p.Renditions[0].ID}); err != nil {
		t.Fatal(err)
	}
	renditions, _ := svc.ListRenditions(p.ID)
	if renditions[0].Name != "1080p" {
		t.Errorf("reorder failed: first is %s", renditions[0].Name)
	}
	// Delete the first.
	if err := svc.DeleteRendition(p.ID, renditions[0].ID); err != nil {
		t.Fatal(err)
	}
	renditions, _ = svc.ListRenditions(p.ID)
	if len(renditions) != 2 {
		t.Errorf("expected 2 renditions after delete, got %d", len(renditions))
	}
}

func TestSkipRenditionsAboveSource(t *testing.T) {
	renditions := []Rendition{
		{Name: "720p", Height: 720, VideoBitrate: "2800k"},
		{Name: "1080p", Height: 1080, VideoBitrate: "5000k"},
		{Name: "2160p", Height: 2160, VideoBitrate: "18000k"},
	}
	out := SkipRenditionsAboveSource(renditions, 1920, 1080)
	if len(out) != 2 {
		t.Errorf("expected 2 renditions ≤1080p, got %d", len(out))
	}
	if out[0].Name != "720p" {
		t.Errorf("expected ascending order, first is %s", out[0].Name)
	}
}

func TestResolveEncoderHardwareFallback(t *testing.T) {
	all := []hwenc.EncoderInfo{
		{ID: "libx264"},
		{ID: "libx265"},
		{ID: "h264_nvenc"},
		{ID: "hevc_nvenc"},
		{ID: "h264_amf"},
		{ID: "hevc_amf"},
	}

	p := &Preset{VideoCodec: "h264_nvenc", HWFallback: true}
	got := ResolveEncoder(p, nil, "")
	if got != "libx264" {
		t.Errorf("expected libx264 fallback, got %s", got)
	}
	got = ResolveEncoder(p, all, "")
	if got != "h264_nvenc" {
		t.Errorf("expected h264_nvenc when available, got %s", got)
	}

	p265 := &Preset{VideoCodec: "hevc_nvenc", HWFallback: true}
	got = ResolveEncoder(p265, nil, "")
	if got != "libx265" {
		t.Errorf("expected libx265 fallback for hevc_nvenc, got %s", got)
	}
	got = ResolveEncoder(p265, all, "")
	if got != "hevc_nvenc" {
		t.Errorf("expected hevc_nvenc when available, got %s", got)
	}

	pAMF := &Preset{VideoCodec: "hevc_amf", HWFallback: true}
	got = ResolveEncoder(pAMF, nil, "")
	if got != "libx265" {
		t.Errorf("expected libx265 fallback for hevc_amf without list, got %s", got)
	}

	soft := &Preset{VideoCodec: "libx265"}
	got = ResolveEncoder(soft, all, "")
	if got != "libx265" {
		t.Errorf("expected libx265 software encoder, got %s", got)
	}
}
