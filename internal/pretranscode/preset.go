// Package pretranscode implements the commercial video pretranscode subsystem
// for knox-media. It manages transcode presets (templates), drives the
// per-rendition worker, integrates AES-128 encryption, dispatches webhooks,
// and exposes the playback-facing status contract defined in coreiface.
package pretranscode

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Preset mirrors SRS 4.1 transcode_preset.
type Preset struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	OutputFormat   string   `json:"output_format"`
	EncryptionMode string   `json:"encryption_mode"`
	VideoCodec     string   `json:"video_codec"`
	VideoPreset    string   `json:"video_preset"`
	VideoCRF       int      `json:"video_crf"`
	VideoMaxrate   string   `json:"video_maxrate"`
	VideoBufsize   string   `json:"video_bufsize"`
	VideoProfile   string   `json:"video_profile"`
	VideoGOP       int      `json:"video_gop"`
	VideoPixFmt    string   `json:"video_pix_fmt"`
	AudioCodec     string   `json:"audio_codec"`
	AudioBitrate   string   `json:"audio_bitrate"`
	AudioChannels  int      `json:"audio_channels"`
	AudioSampleRate int     `json:"audio_sample_rate"`
	HWFallback     bool     `json:"hw_fallback"`
	IsBuiltin      bool     `json:"is_builtin"`
	IsEnabled      bool     `json:"is_enabled"`
	SortOrder      int      `json:"sort_order"`
	OutputDirMode  string   `json:"output_dir_mode"`
	OutputDirCustom string  `json:"output_dir_custom"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	Renditions     []Rendition `json:"renditions,omitempty"`
}

// Rendition mirrors SRS 4.2 preset_rendition.
type Rendition struct {
	ID           int64  `json:"id"`
	PresetID     int64  `json:"preset_id"`
	Name         string `json:"name"`
	Height       int    `json:"height"`
	Width        int    `json:"width"`
	VideoBitrate string `json:"video_bitrate"`
	AudioBitrate string `json:"audio_bitrate"`
	Bandwidth    int    `json:"bandwidth"`
	SortOrder    int    `json:"sort_order"`
}

// PresetService handles preset CRUD.
type PresetService struct{ DB *sql.DB }

// ListPresets returns all presets ordered by sort_order then id.
func (s *PresetService) ListPresets() ([]Preset, error) {
	rows, err := s.DB.Query(`SELECT id, name, COALESCE(description,''), output_format, encryption_mode,
		video_codec, COALESCE(video_preset,''), COALESCE(video_crf,0), COALESCE(video_maxrate,''),
		COALESCE(video_bufsize,''), COALESCE(video_profile,''), COALESCE(video_gop,0), COALESCE(video_pix_fmt,''),
		audio_codec, audio_bitrate, COALESCE(audio_channels,2), COALESCE(audio_sample_rate,48000),
		COALESCE(hw_fallback,1), is_builtin, is_enabled, COALESCE(sort_order,0),
		COALESCE(output_dir_mode,'source'), COALESCE(output_dir_custom,''),
		COALESCE(created_at,''), COALESCE(updated_at,'')
		FROM transcode_preset ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	var out []Preset
	for rows.Next() {
		var p Preset
		var builtin, enabled int
		var hw int
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.OutputFormat, &p.EncryptionMode,
			&p.VideoCodec, &p.VideoPreset, &p.VideoCRF, &p.VideoMaxrate, &p.VideoBufsize,
			&p.VideoProfile, &p.VideoGOP, &p.VideoPixFmt, &p.AudioCodec, &p.AudioBitrate,
			&p.AudioChannels, &p.AudioSampleRate, &hw, &builtin, &enabled, &p.SortOrder,
			&p.OutputDirMode, &p.OutputDirCustom,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.IsBuiltin = builtin == 1
		p.IsEnabled = enabled == 1
		p.HWFallback = hw == 1
		out = append(out, p)
	}
	rows.Close()
	// Fetch renditions after closing the main rows to avoid holding a
	// connection open (problematic with :memory: SQLite pools).
	for i := range out {
		renditions, err := s.ListRenditions(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Renditions = renditions
	}
	return out, nil
}

// GetPreset returns a single preset by id, including renditions.
func (s *PresetService) GetPreset(id int64) (*Preset, error) {
	var p Preset
	var builtin, enabled, hw int
	err := s.DB.QueryRow(`SELECT id, name, COALESCE(description,''), output_format, encryption_mode,
		video_codec, COALESCE(video_preset,''), COALESCE(video_crf,0), COALESCE(video_maxrate,''),
		COALESCE(video_bufsize,''), COALESCE(video_profile,''), COALESCE(video_gop,0), COALESCE(video_pix_fmt,''),
		audio_codec, audio_bitrate, COALESCE(audio_channels,2), COALESCE(audio_sample_rate,48000),
		COALESCE(hw_fallback,1), is_builtin, is_enabled, COALESCE(sort_order,0),
		COALESCE(output_dir_mode,'source'), COALESCE(output_dir_custom,''),
		COALESCE(created_at,''), COALESCE(updated_at,'')
		FROM transcode_preset WHERE id = ?`, id).Scan(
		&p.ID, &p.Name, &p.Description, &p.OutputFormat, &p.EncryptionMode,
		&p.VideoCodec, &p.VideoPreset, &p.VideoCRF, &p.VideoMaxrate, &p.VideoBufsize,
		&p.VideoProfile, &p.VideoGOP, &p.VideoPixFmt, &p.AudioCodec, &p.AudioBitrate,
		&p.AudioChannels, &p.AudioSampleRate, &hw, &builtin, &enabled, &p.SortOrder,
		&p.OutputDirMode, &p.OutputDirCustom,
		&p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrPresetNotFound
	}
	if err != nil {
		return nil, err
	}
	p.IsBuiltin = builtin == 1
	p.IsEnabled = enabled == 1
	p.HWFallback = hw == 1
	renditions, err := s.ListRenditions(id)
	if err != nil {
		return nil, err
	}
	p.Renditions = renditions
	return &p, nil
}

// CreatePresetInput is the payload for creating a new preset.
type CreatePresetInput struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	OutputFormat   string   `json:"output_format"`
	EncryptionMode string   `json:"encryption_mode"`
	VideoCodec     string   `json:"video_codec"`
	VideoPreset    string   `json:"video_preset"`
	VideoCRF       int      `json:"video_crf"`
	VideoMaxrate   string   `json:"video_maxrate"`
	VideoBufsize   string   `json:"video_bufsize"`
	VideoProfile   string   `json:"video_profile"`
	VideoGOP       int      `json:"video_gop"`
	VideoPixFmt    string   `json:"video_pix_fmt"`
	AudioCodec     string   `json:"audio_codec"`
	AudioBitrate   string   `json:"audio_bitrate"`
	AudioChannels  int      `json:"audio_channels"`
	AudioSampleRate int     `json:"audio_sample_rate"`
	HWFallback     bool     `json:"hw_fallback"`
	OutputDirMode  string   `json:"output_dir_mode"`
	OutputDirCustom string  `json:"output_dir_custom"`
	Renditions     []Rendition `json:"renditions"`
}

// CreatePreset inserts a new preset and its renditions. Enforces SRS ENC-05:
// MP4/FLV presets force encryption_mode='none'.
func (s *PresetService) CreatePreset(in CreatePresetInput) (*Preset, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, ErrNameRequired
	}
	if err := validateOutputFormat(in.OutputFormat); err != nil {
		return nil, err
	}
	in.EncryptionMode = normalizeEncryption(in.OutputFormat, in.EncryptionMode)
	if in.VideoCodec == "" {
		in.VideoCodec = "libx264"
	}
	if in.AudioCodec == "" {
		in.AudioCodec = "aac"
	}
	if in.AudioBitrate == "" {
		in.AudioBitrate = "128k"
	}
	if in.AudioChannels == 0 {
		in.AudioChannels = 2
	}
	if in.AudioSampleRate == 0 {
		in.AudioSampleRate = 48000
	}
	if in.VideoPixFmt == "" {
		in.VideoPixFmt = "yuv420p"
	}
	if in.OutputDirMode == "" {
		in.OutputDirMode = OutputDirModeSource
	}
	in.OutputDirMode = NormalizeOutputDirMode(in.OutputDirMode)
	hw := 1
	if !in.HWFallback {
		hw = 0
	}
	res, err := s.DB.Exec(`INSERT INTO transcode_preset
		(name, description, output_format, encryption_mode, video_codec, video_preset, video_crf,
		 video_maxrate, video_bufsize, video_profile, video_gop, video_pix_fmt,
		 audio_codec, audio_bitrate, audio_channels, audio_sample_rate, hw_fallback,
		 output_dir_mode, output_dir_custom, is_builtin, is_enabled, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 1, 0)`,
		in.Name, in.Description, in.OutputFormat, in.EncryptionMode, in.VideoCodec, in.VideoPreset, in.VideoCRF,
		in.VideoMaxrate, in.VideoBufsize, in.VideoProfile, in.VideoGOP, in.VideoPixFmt,
		in.AudioCodec, in.AudioBitrate, in.AudioChannels, in.AudioSampleRate, hw,
		in.OutputDirMode, in.OutputDirCustom)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	for i, r := range in.Renditions {
		if err := s.insertRendition(id, &r, i); err != nil {
			return nil, err
		}
	}
	return s.GetPreset(id)
}

// UpdatePreset mutates an existing preset. Builtin presets may have their
// parameters edited but cannot be deleted (DeletePreset enforces that).
func (s *PresetService) UpdatePreset(id int64, in CreatePresetInput) (*Preset, error) {
	if _, err := s.GetPreset(id); err != nil {
		return nil, err
	}
	if err := validateOutputFormat(in.OutputFormat); err != nil {
		return nil, err
	}
	in.EncryptionMode = normalizeEncryption(in.OutputFormat, in.EncryptionMode)
	if in.OutputDirMode == "" {
		in.OutputDirMode = OutputDirModeSource
	}
	in.OutputDirMode = NormalizeOutputDirMode(in.OutputDirMode)
	hw := 1
	if !in.HWFallback {
		hw = 0
	}
	_, err := s.DB.Exec(`UPDATE transcode_preset SET
		name=?, description=?, output_format=?, encryption_mode=?, video_codec=?, video_preset=?, video_crf=?,
		video_maxrate=?, video_bufsize=?, video_profile=?, video_gop=?, video_pix_fmt=?,
		audio_codec=?, audio_bitrate=?, audio_channels=?, audio_sample_rate=?, hw_fallback=?,
		output_dir_mode=?, output_dir_custom=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.Name, in.Description, in.OutputFormat, in.EncryptionMode, in.VideoCodec, in.VideoPreset, in.VideoCRF,
		in.VideoMaxrate, in.VideoBufsize, in.VideoProfile, in.VideoGOP, in.VideoPixFmt,
		in.AudioCodec, in.AudioBitrate, in.AudioChannels, in.AudioSampleRate, hw,
		in.OutputDirMode, in.OutputDirCustom, id)
	if err != nil {
		return nil, err
	}
	// Replace renditions.
	_, _ = s.DB.Exec(`DELETE FROM preset_rendition WHERE preset_id = ?`, id)
	for i, r := range in.Renditions {
		if err := s.insertRendition(id, &r, i); err != nil {
			return nil, err
		}
	}
	return s.GetPreset(id)
}

// DeletePreset removes a non-builtin preset (SRS TPL-03).
func (s *PresetService) DeletePreset(id int64) error {
	var builtin int
	err := s.DB.QueryRow(`SELECT is_builtin FROM transcode_preset WHERE id = ?`, id).Scan(&builtin)
	if err == sql.ErrNoRows {
		return ErrPresetNotFound
	}
	if err != nil {
		return err
	}
	if builtin == 1 {
		return ErrBuiltinProtected
	}
	_, err = s.DB.Exec(`DELETE FROM transcode_preset WHERE id = ?`, id)
	return err
}

// ClonePreset duplicates a preset (and its renditions) with a new name.
func (s *PresetService) ClonePreset(id int64, newName string) (*Preset, error) {
	src, err := s.GetPreset(id)
	if err != nil {
		return nil, err
	}
	if newName == "" {
		newName = src.Name + " (copy)"
	}
	in := CreatePresetInput{
		Name: newName, Description: src.Description,
		OutputFormat: src.OutputFormat, EncryptionMode: src.EncryptionMode,
		VideoCodec: src.VideoCodec, VideoPreset: src.VideoPreset, VideoCRF: src.VideoCRF,
		VideoMaxrate: src.VideoMaxrate, VideoBufsize: src.VideoBufsize, VideoProfile: src.VideoProfile,
		VideoGOP: src.VideoGOP, VideoPixFmt: src.VideoPixFmt,
		AudioCodec: src.AudioCodec, AudioBitrate: src.AudioBitrate,
		AudioChannels: src.AudioChannels, AudioSampleRate: src.AudioSampleRate,
		HWFallback: src.HWFallback,
		OutputDirMode: src.OutputDirMode, OutputDirCustom: src.OutputDirCustom,
	}
	for _, r := range src.Renditions {
		in.Renditions = append(in.Renditions, Rendition{
			Name: r.Name, Height: r.Height, Width: r.Width,
			VideoBitrate: r.VideoBitrate, AudioBitrate: r.AudioBitrate,
			Bandwidth: r.Bandwidth, SortOrder: r.SortOrder,
		})
	}
	return s.CreatePreset(in)
}

// TogglePreset flips is_enabled.
func (s *PresetService) TogglePreset(id int64) (bool, error) {
	var enabled int
	err := s.DB.QueryRow(`SELECT is_enabled FROM transcode_preset WHERE id = ?`, id).Scan(&enabled)
	if err == sql.ErrNoRows {
		return false, ErrPresetNotFound
	}
	if err != nil {
		return false, err
	}
	newVal := 1
	if enabled == 1 {
		newVal = 0
	}
	_, err = s.DB.Exec(`UPDATE transcode_preset SET is_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, newVal, id)
	return newVal == 1, err
}

// ListRenditions returns renditions for a preset ordered by sort_order.
func (s *PresetService) ListRenditions(presetID int64) ([]Rendition, error) {
	rows, err := s.DB.Query(`SELECT id, preset_id, name, height, COALESCE(width,0), video_bitrate,
		COALESCE(audio_bitrate,''), COALESCE(bandwidth,0), COALESCE(sort_order,0)
		FROM preset_rendition WHERE preset_id = ? ORDER BY sort_order, id`, presetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rendition
	for rows.Next() {
		var r Rendition
		if err := rows.Scan(&r.ID, &r.PresetID, &r.Name, &r.Height, &r.Width, &r.VideoBitrate,
			&r.AudioBitrate, &r.Bandwidth, &r.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// AddRendition appends a rendition to a preset.
func (s *PresetService) AddRendition(presetID int64, r Rendition) (*Rendition, error) {
	var nextOrder int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(sort_order),-1) + 1 FROM preset_rendition WHERE preset_id = ?`, presetID).Scan(&nextOrder)
	if err := s.insertRendition(presetID, &r, nextOrder); err != nil {
		return nil, err
	}
	return &r, nil
}

// UpdateRendition mutates a rendition row.
func (s *PresetService) UpdateRendition(presetID, renditionID int64, r Rendition) (*Rendition, error) {
	_, err := s.DB.Exec(`UPDATE preset_rendition SET name=?, height=?, width=?, video_bitrate=?, audio_bitrate=?, bandwidth=?, sort_order=?, video_rate=?, audio_rate=? WHERE id=? AND preset_id=?`,
		r.Name, r.Height, r.Width, r.VideoBitrate, r.AudioBitrate, r.Bandwidth, r.SortOrder, r.VideoBitrate, r.AudioBitrate, renditionID, presetID)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// DeleteRendition removes a rendition.
func (s *PresetService) DeleteRendition(presetID, renditionID int64) error {
	_, err := s.DB.Exec(`DELETE FROM preset_rendition WHERE id = ? AND preset_id = ?`, renditionID, presetID)
	return err
}

// SortRenditions reorders renditions by the given id sequence (SRS REN-06).
func (s *PresetService) SortRenditions(presetID int64, orderedIDs []int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	for i, id := range orderedIDs {
		if _, err := tx.Exec(`UPDATE preset_rendition SET sort_order = ? WHERE id = ? AND preset_id = ?`, i, id, presetID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *PresetService) insertRendition(presetID int64, r *Rendition, order int) error {
	if r.VideoBitrate == "" {
		return ErrRenditionBitrateRequired
	}
	res, err := s.DB.Exec(`INSERT INTO preset_rendition (preset_id, name, height, width, video_bitrate, audio_bitrate, bandwidth, sort_order, video_rate, audio_rate)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		presetID, r.Name, r.Height, r.Width, r.VideoBitrate, r.AudioBitrate, r.Bandwidth, order, r.VideoBitrate, r.AudioBitrate)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	r.PresetID = presetID
	r.SortOrder = order
	return nil
}

// validateOutputFormat enforces SRS FMT-01..04 (hls/mp4/dash/flv only).
func validateOutputFormat(f string) error {
	switch f {
	case "hls", "mp4", "dash", "flv":
		return nil
	}
	return fmt.Errorf("invalid output_format %q", f)
}

// normalizeEncryption enforces SRS ENC-05: MP4/FLV must be 'none'.
func normalizeEncryption(format, enc string) string {
	if format == "mp4" || format == "flv" {
		return "none"
	}
	switch enc {
	case "none", "aes128", "powerdrm", "drm":
		return enc
	}
	return "none"
}

// SkipRenditionsAboveSource returns the subset of renditions whose height is
// <= sourceHeight (SRS REN-05: never upscale).
func SkipRenditionsAboveSource(renditions []Rendition, sourceHeight int) []Rendition {
	out := make([]Rendition, 0, len(renditions))
	for _, r := range renditions {
		if sourceHeight <= 0 || r.Height <= sourceHeight {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Height < out[j].Height })
	return out
}

// Standard errors.
var (
	ErrPresetNotFound         = errors.New("preset not found")
	ErrBuiltinProtected       = errors.New("builtin presets cannot be deleted")
	ErrNameRequired           = errors.New("preset name is required")
	ErrRenditionBitrateRequired = errors.New("rendition video_bitrate is required")
)
