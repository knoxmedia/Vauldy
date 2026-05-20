package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server       ServerConfig             `yaml:"server"`
	Data         DataConfig               `yaml:"data"`
	Security     SecurityConfig           `yaml:"security"`
	FFmpeg       FFmpegConfig             `yaml:"ffmpeg"`
	DRMPackaging DRMPackagingConfig       `yaml:"drm_packaging"`
	DRM          DRMConfig                `yaml:"drm"`
	Scan         ScanConfig               `yaml:"scan"`
	Subtitle     SubtitleProcessingConfig `yaml:"subtitle"`
	ATrack       ATrackConfig             `yaml:"atrack"`
	JIT          JITConfig                `yaml:"jit"`
	CORS         CORSConfig               `yaml:"cors"`
	// PowerPlayer is included in GET /media/:id/hls playback plans for the web player (PowerPlayer 6 setup).
	PowerPlayer PowerPlayerWebConfig `yaml:"powerplayer"`
	// Playback selects web player priority (see api/handler play.HLSInfo player_engine_order).
	Playback PlaybackConfig `yaml:"playback"`
}

// PlaybackConfig controls browser player engine order in the playback plan JSON.
type PlaybackConfig struct {
	Engines PlaybackEnginesYAML `yaml:"engines"`
}

// PlaybackEnginesYAML lists engine ids: powerplayer | shaka | xgplayer.
type PlaybackEnginesYAML struct {
	// ProgressiveHLS: native direct file + clear HLS (hls, jit_hls, hls_aes_128).
	ProgressiveHLS []string `yaml:"progressive_hls"`
	HLSPowerDRM    []string `yaml:"hls_powerdrm"`
	DRM            []string `yaml:"drm"` // Widevine/FairPlay (hls_drm / DASH)
}

// PowerPlayerWebConfig maps to PowerPlayer 6 .setup() fields (baseUrl, skin, powerdrmurl, etc.).
type PowerPlayerWebConfig struct {
	BaseURL          string `yaml:"base_url"`
	Skin             string `yaml:"skin"`
	PowerDRMURL      string `yaml:"powerdrm_url"`
	WebURLParam      string `yaml:"weburlparam"`
	StatisticsServer string `yaml:"statistics_server"`
	ClientCert       string `yaml:"client_cert"`
}

// ScanConfig tunes library scan performance (ffprobe / optional file hashing).
type ScanConfig struct {
	// FileHashOnScan computes MD5 of each media file during scan for deduplication. Very slow on large files; default off.
	FileHashOnScan *bool `yaml:"file_hash_on_scan"`
	// FastFFprobe limits analyzeduration/probesize during scan metadata reads. Default on; set false if metadata is incomplete.
	FastFFprobe *bool `yaml:"fast_ffprobe"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	Mode string `yaml:"mode"`
	// HLSMultiAudioEnabled controls whether the JIT master playlist may emit
	// EXT-X-MEDIA TYPE=AUDIO groups for pre-extracted audio tracks. When disabled,
	// audio is always embedded in the video TS (traditional single-muxed behavior).
	HLSMultiAudioEnabled *bool `yaml:"hls_multi_audio_enabled"`
}

type DataConfig struct {
	Dir       string `yaml:"dir"`
	DB        string `yaml:"db"`
	Transcode string `yaml:"transcode"`
	Preview   string `yaml:"preview"`
	Subtitle  string `yaml:"subtitle"`
	Upload    string `yaml:"upload"`
	Chunks    string `yaml:"chunks"`
	ATracks   string `yaml:"atracks"`
	Keyframes string `yaml:"keyframes"`
	// MetadataLibrary is the filesystem root for scraped posters/backdrops/logos (HTTP /metadata/library/…).
	// Default: {dir}/metadata/library (e.g. /data/metadata/library when data.dir is /data).
	MetadataLibrary string `yaml:"metadata_library"`
	// Static is the filesystem root for HTTP path /static/ (e.g. PowerPlayer assets under static/powerplayer6/).
	Static string `yaml:"static"`
}

// SubtitleProcessing configures post-processing (sidecar scan, embedded extract, optional ASR).
type SubtitleProcessingConfig struct {
	// AutoOnScan inserts a pending subtitle_task when library scan discovers a new video.
	// Nil means true (backward compatible with older config files).
	AutoOnScan   *bool              `yaml:"auto_on_scan"`
	ASR          ASRConfig          `yaml:"asr"`
	GraphicalOCR GraphicalOCRConfig `yaml:"graphical_ocr"`
}

// ATrackConfig controls audio track extraction behavior.
type ATrackConfig struct {
	// AutoOnScan inserts a waiting atrack_task when library scan discovers a new video.
	AutoOnScan *bool `yaml:"auto_on_scan"`
}

// JITConfig controls just-in-time transcode behavior.
type JITConfig struct {
	// ContinuousHLSEnabled when true, the transcodeworker uses a single long-running
	// ffmpeg process with -f segment muxer to output all TS segments for a bitrate,
	// instead of launching one ffmpeg per segment. Reduces overhead and avoids
	// keyframe bursts at segment boundaries. Default: true.
	ContinuousHLSEnabled *bool `yaml:"continuous_hls_enabled"`
}

// GraphicalOCR enables Tesseract-based OCR for bitmap subtitles (PGS, VobSub, etc.).
type GraphicalOCRConfig struct {
	Enabled        bool   `yaml:"enabled"`
	TesseractPath  string `yaml:"tesseract_path"`
	TessdataPrefix string `yaml:"tessdata_prefix"`
	Languages      string `yaml:"languages"`
	PythonPath     string `yaml:"python_path"`
	ScriptPath     string `yaml:"script_path"`
	PgsripPath     string `yaml:"pgsrip_path"`
	MkvextractPath string `yaml:"mkvextract_path"`
	MkvmergePath   string `yaml:"mkvmerge_path"`
}

// ASRConfig optional speech-to-text when no subtitles are present.
type ASRConfig struct {
	Provider    string   `yaml:"provider"`
	WhisperPath string   `yaml:"whisper_path"`
	ExtraArgs   []string `yaml:"extra_args"`
	Shell       string   `yaml:"shell"`
}

type SecurityConfig struct {
	JWTSecret  string `yaml:"jwt_secret"`
	TokenHours int    `yaml:"token_hours"`
	KIDVersion string `yaml:"kid_version"`
	SigVersion string `yaml:"sig_version"`
}

type FFmpegConfig struct {
	FFprobePath string `yaml:"ffprobe_path"`
	FFmpegPath  string `yaml:"ffmpeg_path"`
}

// DRMPackagingConfig selects how CENC fMP4 HLS is produced. EngineOrder lists packagers
// in priority: first is tried, then the next on failure (e.g. shaka, ffmpeg).
type DRMPackagingConfig struct {
	// EngineOrder allows only "shaka" and "ffmpeg" (case-insensitive, duplicates removed).
	// If empty, defaults to shaka then ffmpeg.
	EngineOrder []string `yaml:"engine_order"`
	// ShakaPackagerPath to the shaka/packager binary. Required for the shaka engine; empty skips shaka and uses the next engine.
	ShakaPackagerPath string `yaml:"shaka_packager_path"`
	// SegmentSeconds is HLS segment / chunk length in seconds; default 4.
	SegmentSeconds int `yaml:"segment_seconds"`
}

type CORSConfig struct {
	AllowOrigins []string `yaml:"allow_origins"`
}

// DRMConfig contains runtime DRM service integration settings.
type DRMConfig struct {
	Widevine WidevineConfig `yaml:"widevine"`
	PowerDRM PowerDRMConfig `yaml:"powerdrm"`
}

type WidevineConfig struct {
	// Enabled controls whether DRM/Widevine packaging mode is available.
	Enabled *bool `yaml:"enabled"`
	// EmitServiceCertURL when true adds widevine_service_cert_url to the HLS DRM
	// playback plan (requires private_module_url). Default false: CDM uses in-band
	// SERVICE_CERTIFICATE_REQUEST on the license endpoint instead.
	EmitServiceCertURL bool `yaml:"emit_service_cert_url"`
	// PrivateModuleURL points to a privately deployed compliant widevine
	// license module. When configured, backend uses raw challenge passthrough.
	PrivateModuleURL string `yaml:"private_module_url"`
	// Optional bearer token for the private module.
	PrivateModuleToken string `yaml:"private_module_token"`
	// Request timeout to private module.
	PrivateModuleTimeoutSeconds int `yaml:"private_module_timeout_seconds"`
}

type PowerDRMConfig struct {
	// Enabled rewrites packaged HLS EXT-X-KEY to PowerDRM format.
	Enabled bool `yaml:"enabled"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8200
	}
	if c.Data.Dir == "" {
		c.Data.Dir = "./data"
	}
	if c.Data.DB == "" {
		c.Data.DB = filepath.Join(c.Data.Dir, "knox-media.db")
	}
	if c.Data.Transcode == "" {
		c.Data.Transcode = filepath.Join(c.Data.Dir, "transcode")
	}
	if c.Data.Upload == "" {
		c.Data.Upload = filepath.Join(c.Data.Dir, "upload")
	}
	if c.Data.Preview == "" {
		c.Data.Preview = filepath.Join(c.Data.Dir, "preview")
	}
	if c.Data.Subtitle == "" {
		c.Data.Subtitle = filepath.Join(c.Data.Dir, "subtitles")
	}
	if c.Data.Chunks == "" {
		c.Data.Chunks = filepath.Join(c.Data.Upload, "chunks")
	}
	if c.Data.ATracks == "" {
		c.Data.ATracks = filepath.Join(c.Data.Dir, "atracks")
	}
	if c.Data.Keyframes == "" {
		c.Data.Keyframes = filepath.Join(c.Data.Dir, "keyframes")
	}
	if c.Data.Static == "" {
		c.Data.Static = filepath.Join(c.Data.Dir, "static")
	}
	if c.Data.MetadataLibrary == "" {
		c.Data.MetadataLibrary = filepath.Join(c.Data.Dir, "metadata", "library")
	}
	if c.Security.TokenHours == 0 {
		c.Security.TokenHours = 168
	}
	if c.Security.JWTSecret == "" {
		c.Security.JWTSecret = "knox-media-dev-secret"
	}
	if c.Security.KIDVersion == "" {
		c.Security.KIDVersion = "v1"
	}
	if c.Security.SigVersion == "" {
		c.Security.SigVersion = "hmac-sha256-v1"
	}
	if c.FFmpeg.FFprobePath == "" {
		c.FFmpeg.FFprobePath = "ffprobe"
	}
	if c.FFmpeg.FFmpegPath == "" {
		c.FFmpeg.FFmpegPath = "ffmpeg"
	}
	if c.Subtitle.AutoOnScan == nil {
		t := true
		c.Subtitle.AutoOnScan = &t
	}
	if c.ATrack.AutoOnScan == nil {
		t := true
		c.ATrack.AutoOnScan = &t
	}
	if c.JIT.ContinuousHLSEnabled == nil {
		t := true
		c.JIT.ContinuousHLSEnabled = &t
	}
	if c.Server.HLSMultiAudioEnabled == nil {
		t := true
		c.Server.HLSMultiAudioEnabled = &t
	}
	if c.Scan.FastFFprobe == nil {
		t := true
		c.Scan.FastFFprobe = &t
	}
	if c.Scan.FileHashOnScan == nil {
		f := false
		c.Scan.FileHashOnScan = &f
	}
	c.normalizeDRMPackaging()
	if c.DRM.Widevine.Enabled == nil {
		t := true
		c.DRM.Widevine.Enabled = &t
	}
	if c.DRM.Widevine.PrivateModuleTimeoutSeconds <= 0 {
		c.DRM.Widevine.PrivateModuleTimeoutSeconds = 8
	}
	return &c, nil
}

// NormalizeDRMPackagingOrder coerces engine list to a stable order. Unknown entries
// are dropped. Empty or nil input yields default: shaka, then ffmpeg.
func NormalizeDRMPackagingOrder(order []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, x := range order {
		s := strings.ToLower(strings.TrimSpace(x))
		if s != "shaka" && s != "ffmpeg" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return []string{"shaka", "ffmpeg"}
	}
	return out
}

func (c *Config) normalizeDRMPackaging() {
	if c.DRMPackaging.SegmentSeconds <= 0 {
		c.DRMPackaging.SegmentSeconds = 4
	}
	c.DRMPackaging.EngineOrder = NormalizeDRMPackagingOrder(c.DRMPackaging.EngineOrder)
}

// LibraryScanFileHash reports whether the scanner should MD5 whole files (slow).
func (c *Config) LibraryScanFileHash() bool {
	if c == nil {
		return false
	}
	return c.Scan.FileHashOnScan != nil && *c.Scan.FileHashOnScan
}

// LibraryScanFastFFprobe reports whether to use shorter ffprobe analysis during library scan.
func (c *Config) LibraryScanFastFFprobe() bool {
	if c == nil {
		return true
	}
	if c.Scan.FastFFprobe == nil {
		return true
	}
	return *c.Scan.FastFFprobe
}

// SubtitleAutoOnScan reports whether scan should enqueue pending subtitle tasks for new videos.
func (c *Config) SubtitleAutoOnScan() bool {
	if c == nil {
		return false
	}
	if c.Subtitle.AutoOnScan == nil {
		return true
	}
	return *c.Subtitle.AutoOnScan
}

// ATrackAutoOnScan reports whether scan should enqueue atrack tasks for new videos.
func (c *Config) ATrackAutoOnScan() bool {
	if c == nil {
		return false
	}
	if c.ATrack.AutoOnScan == nil {
		return true
	}
	return *c.ATrack.AutoOnScan
}

// HLSMultiAudioEnabled reports whether JIT master playlist may emit separate audio groups.
func (c *Config) HLSMultiAudioEnabled() bool {
	if c == nil {
		return false
	}
	if c.Server.HLSMultiAudioEnabled == nil {
		return true
	}
	return *c.Server.HLSMultiAudioEnabled
}

// JITContinuousHLSEnabled reports whether continuous HLS transcode mode is active.
func (c *Config) JITContinuousHLSEnabled() bool {
	if c == nil {
		return false
	}
	if c.JIT.ContinuousHLSEnabled == nil {
		return true
	}
	return *c.JIT.ContinuousHLSEnabled
}

func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

func (c *Config) EnsureDirs() error {
	for _, d := range []string{c.Data.Dir, c.Data.Transcode, c.Data.Preview, c.Data.Subtitle, c.Data.Upload, c.Data.Chunks, c.Data.ATracks, c.Data.Keyframes, c.Data.Static, c.Data.MetadataLibrary} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// ResolveExecutablePaths makes configured executable paths absolute using baseDir
// (typically the directory containing config.yml). This keeps relative tool paths
// stable regardless of process working directory.
func (c *Config) ResolveExecutablePaths(baseDir string) {
	if c == nil || strings.TrimSpace(baseDir) == "" {
		return
	}
	c.FFmpeg.FFmpegPath = resolveMaybeRelativePath(c.FFmpeg.FFmpegPath, baseDir)
	c.FFmpeg.FFprobePath = resolveMaybeRelativePath(c.FFmpeg.FFprobePath, baseDir)
	c.DRMPackaging.ShakaPackagerPath = resolveMaybeRelativePath(c.DRMPackaging.ShakaPackagerPath, baseDir)
}

func resolveMaybeRelativePath(p string, baseDir string) string {
	s := strings.TrimSpace(p)
	if s == "" || filepath.IsAbs(s) {
		return s
	}
	return filepath.Clean(filepath.Join(baseDir, s))
}

func (c *Config) WidevineEnabled() bool {
	if c == nil || c.DRM.Widevine.Enabled == nil {
		return true
	}
	return *c.DRM.Widevine.Enabled
}
