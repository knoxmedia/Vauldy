package taskcontrol

import "sort"

// commonTaskColumns returns the baseline columns shared across most task types.
func commonTaskColumns() []ColumnSpec {
	return []ColumnSpec{
		{Key: "attempts", Label: "tasks.column.attempts"},
		{Key: "available_at", Label: "tasks.column.available_at"},
		{Key: "created_at", Label: "tasks.column.created_at"},
		{Key: "generation", Label: "tasks.column.generation"},
		{Key: "id", Label: "tasks.column.id"},
		{Key: "library_id", Label: "tasks.column.library_id"},
		{Key: "media_id", Label: "tasks.column.media_id"},
		{Key: "priority", Label: "tasks.column.priority"},
		{Key: "retry_round", Label: "tasks.column.retry_round"},
		{Key: "status", Label: "tasks.column.status"},
		{Key: "updated_at", Label: "tasks.column.updated_at"},
	}
}

// commonTaskFilters returns the baseline filters shared across most task types.
func commonTaskFilters() []FilterSpec {
	return []FilterSpec{
		{Key: "generation", Label: "tasks.filter.generation"},
		{Key: "library_id", Label: "tasks.filter.library_id"},
		{Key: "media_id", Label: "tasks.filter.media_id"},
		{Key: "priority", Label: "tasks.filter.priority"},
		{Key: "removed", Label: "tasks.filter.removed", Values: []string{"exclude", "include", "only"}},
		{Key: "retry_round", Label: "tasks.filter.retry_round"},
		{Key: "status", Label: "tasks.filter.status"},
	}
}

// sortedColumns returns a copy of the columns sorted by key.
func sortedColumns(cols []ColumnSpec) []ColumnSpec {
	result := make([]ColumnSpec, len(cols))
	copy(result, cols)
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

// sortedFilters returns a copy of the filters sorted by key.
func sortedFilters(f []FilterSpec) []FilterSpec {
	result := make([]FilterSpec, len(f))
	copy(result, f)
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

// sortedStrings returns a sorted copy of the given strings.
func sortedStrings(s []string) []string {
	result := make([]string, len(s))
	copy(result, s)
	sort.Strings(result)
	return result
}

// NewRegistry builds the immutable, ordered task type registry.
// Group labels are non-selectable i18n keys.
// Phase 5 types are visible with available=false.
// Routes, columns, filters, and capabilities are deterministic and unique.
// Source mappings map Phase 1-3 implementation names to public stable type names.
func NewRegistry() *Registry {
	var groups []TaskGroup

	// Overview group (first, non-selectable)
	groups = append(groups, TaskGroup{
		Label:      "tasks.group.overview",
		Selectable: false,
		Types:      nil,
	})

	// Video Post-Processing group
	groups = append(groups, TaskGroup{
		Label:      "tasks.group.video",
		Selectable: false,
		Types: []TaskSpec{
			{
				Type:  "transcode",
				Group: "tasks.group.video",
				Route: "tasks/video/transcode",
				Family: "video_post_processing",
				SourceMappings: []SourceMapping{
					{Kind: "transcode_task"},
				},
				Columns: sortedColumns(append(commonTaskColumns(),
					ColumnSpec{Key: "output_path", Label: "tasks.column.output_path"},
					ColumnSpec{Key: "quality", Label: "tasks.column.quality"},
					ColumnSpec{Key: "source_file", Label: "tasks.column.source_file"},
				)),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"encoding", "video"}),
				Available:    true,
			},
			{
				Type:  "optimize",
				Group: "tasks.group.video",
				Route: "tasks/video/optimize",
				Family: "video_post_processing",
				SourceMappings: []SourceMapping{
					{Kind: "optimize_task"},
				},
				Columns: sortedColumns(append(commonTaskColumns(),
					ColumnSpec{Key: "output_path", Label: "tasks.column.output_path"},
					ColumnSpec{Key: "source_file", Label: "tasks.column.source_file"},
				)),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"encoding", "optimization", "video"}),
				Available:    true,
			},
			{
				Type:  "package",
				Group: "tasks.group.video",
				Route: "tasks/video/package",
				Family: "video_post_processing",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "package"},
					{Kind: "package_task"},
				},
				Columns: sortedColumns(append(commonTaskColumns(),
					ColumnSpec{Key: "drm_status", Label: "tasks.column.drm_status"},
					ColumnSpec{Key: "output_path", Label: "tasks.column.output_path"},
					ColumnSpec{Key: "pipeline_type", Label: "tasks.column.pipeline_type"},
				)),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"packaging", "video"}),
				Available:    true,
			},
			{
				Type:  "encrypt",
				Group: "tasks.group.video",
				Route: "tasks/video/encrypt",
				Family: "video_post_processing",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "encrypt"},
				},
				Columns: sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"drm", "encryption", "video"}),
				Available:    true,
			},
			{
				Type:  "pretranscode",
				Group: "tasks.group.video",
				Route: "tasks/video/pretranscode",
				Family: "video_post_processing",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "pretranscode"},
				},
				Columns: sortedColumns(append(commonTaskColumns(),
					ColumnSpec{Key: "output_path", Label: "tasks.column.output_path"},
				)),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"encoding", "video"}),
				Available:    true,
			},
		},
	})

	// Media Ingestion group
	groups = append(groups, TaskGroup{
		Label:      "tasks.group.ingestion",
		Selectable: false,
		Types: []TaskSpec{
			{
				Type:  "poster",
				Group: "tasks.group.ingestion",
				Route: "tasks/ingestion/poster",
				Family: "media_ingestion",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "poster"},
					{Kind: "post_ingest_task", InternalType: "poster_repair"},
				},
				Columns: sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"image", "poster"}),
				Available:    true,
			},
			{
				Type:  "thumbnail",
				Group: "tasks.group.ingestion",
				Route: "tasks/ingestion/thumbnail",
				Family: "media_ingestion",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "thumbnail"},
				},
				Columns: sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"image", "thumbnail"}),
				Available:    true,
			},
			{
				Type:  "preview",
				Group: "tasks.group.ingestion",
				Route: "tasks/ingestion/preview",
				Family: "media_ingestion",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "preview"},
					{Kind: "preview_task"},
				},
				Columns: sortedColumns(append(commonTaskColumns(),
					ColumnSpec{Key: "interval_sec", Label: "tasks.column.interval_sec"},
					ColumnSpec{Key: "sprite_path", Label: "tasks.column.sprite_path"},
					ColumnSpec{Key: "thumb_count", Label: "tasks.column.thumb_count"},
					ColumnSpec{Key: "thumb_height", Label: "tasks.column.thumb_height"},
					ColumnSpec{Key: "thumb_width", Label: "tasks.column.thumb_width"},
					ColumnSpec{Key: "vtt_path", Label: "tasks.column.vtt_path"},
				)),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"image", "sprite", "video"}),
				Available:    true,
			},
			{
				Type:  "keyframe",
				Group: "tasks.group.ingestion",
				Route: "tasks/ingestion/keyframe",
				Family: "media_ingestion",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "keyframe"},
					{Kind: "post_ingest_task", InternalType: "keyframe_extract"},
					{Kind: "keyframe_task"},
				},
				Columns: sortedColumns(append(commonTaskColumns(),
					ColumnSpec{Key: "keyframe_count", Label: "tasks.column.keyframe_count"},
					ColumnSpec{Key: "output_dir", Label: "tasks.column.output_dir"},
				)),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"image", "video"}),
				Available:    true,
			},
			{
				Type:  "subtitle_extract",
				Group: "tasks.group.ingestion",
				Route: "tasks/ingestion/subtitle_extract",
				Family: "media_ingestion",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "subtitle_extract"},
				},
				Columns: sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"subtitle", "text"}),
				Available:    true,
			},
			{
				Type:  "subtitle_recognize",
				Group: "tasks.group.ingestion",
				Route: "tasks/ingestion/subtitle_recognize",
				Family: "media_ingestion",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "subtitle_recognize"},
				},
				Columns: sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"asr", "speech", "text"}),
				Available:    true,
			},
			{
				Type:  "atrack_extract",
				Group: "tasks.group.ingestion",
				Route: "tasks/ingestion/atrack_extract",
				Family: "media_ingestion",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "atrack"},
					{Kind: "post_ingest_task", InternalType: "atrack_extract"},
					{Kind: "atrack_task"},
				},
				Columns: sortedColumns(append(commonTaskColumns(),
					ColumnSpec{Key: "output_dir", Label: "tasks.column.output_dir"},
				)),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"audio", "extraction"}),
				Available:    true,
			},
			{
				Type:  "metadata_scrape",
				Group: "tasks.group.ingestion",
				Route: "tasks/ingestion/metadata_scrape",
				Family: "media_ingestion",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "metadata"},
					{Kind: "scrape_task"},
				},
				Columns: sortedColumns(append(commonTaskColumns(),
					ColumnSpec{Key: "query", Label: "tasks.column.query"},
					ColumnSpec{Key: "source", Label: "tasks.column.source"},
					ColumnSpec{Key: "task_type", Label: "tasks.column.task_type"},
					ColumnSpec{Key: "year", Label: "tasks.column.year"},
				)),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"metadata", "network"}),
				Available:    true,
			},
			{
				Type:  "ai_analysis",
				Group: "tasks.group.ingestion",
				Route: "tasks/ingestion/ai_analysis",
				Family: "media_ingestion",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "ai_analysis"},
				},
				Columns: sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"ai", "analysis"}),
				Available:    true,
			},
			{
				Type:  "media_visible",
				Group: "tasks.group.ingestion",
				Route: "tasks/ingestion/media_visible",
				Family: "media_ingestion",
				SourceMappings: []SourceMapping{
					{Kind: "post_ingest_task", InternalType: "media_visible"},
				},
				Columns:      sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"publication"}),
				Available:    true,
			},
		},
	})

	// Image Processing group
	groups = append(groups, TaskGroup{
		Label:      "tasks.group.image",
		Selectable: false,
		Types: []TaskSpec{
			{
				Type:  "photo_classify",
				Group: "tasks.group.image",
				Route: "tasks/image/photo_classify",
				Family: "image_processing",
				SourceMappings: []SourceMapping{
					{Kind: "photo_classify_task"},
				},
				Columns:      sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"ai", "classification", "image"}),
				Available:    true,
			},
			{
				Type:  "photo_geocode",
				Group: "tasks.group.image",
				Route: "tasks/image/photo_geocode",
				Family: "image_processing",
				SourceMappings: []SourceMapping{
					{Kind: "photo_geocode_task"},
				},
				Columns:      sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"geo", "image"}),
				Available:    true,
			},
			{
				Type:  "photo_face",
				Group: "tasks.group.image",
				Route: "tasks/image/photo_face",
				Family: "image_processing",
				SourceMappings: []SourceMapping{
					{Kind: "photo_face_task"},
				},
				Columns:      sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"ai", "face", "image"}),
				Available:    true,
			},
			{
				Type:  "image_ocr",
				Group: "tasks.group.image",
				Route: "tasks/image/image_ocr",
				Family: "image_processing",
				SourceMappings: []SourceMapping{
					{Kind: "image_ocr_task"},
				},
				Columns:      sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"ai", "image", "ocr"}),
				Available:    false,
			},
		},
	})

	// Audio Processing group (Phase 5 types)
	groups = append(groups, TaskGroup{
		Label:      "tasks.group.audio",
		Selectable: false,
		Types: []TaskSpec{
			{
				Type:  "lyric",
				Group: "tasks.group.audio",
				Route: "tasks/audio/lyric",
				Family: "audio_processing",
				SourceMappings: []SourceMapping{
					{Kind: "lyric_task"},
				},
				Columns: sortedColumns(append(commonTaskColumns(),
					ColumnSpec{Key: "lrc_path", Label: "tasks.column.lrc_path"},
					ColumnSpec{Key: "vtt_path", Label: "tasks.column.vtt_path"},
				)),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"ai", "audio", "lyrics"}),
				Available:    true,
			},
			{
				Type:  "audio_analysis",
				Group: "tasks.group.audio",
				Route: "tasks/audio/audio_analysis",
				Family: "audio_processing",
				SourceMappings: []SourceMapping{
					{Kind: "audio_analysis_task"},
				},
				Columns:      sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"ai", "audio", "classification"}),
				Available:    false,
			},
			{
				Type:  "audio_ai_analysis",
				Group: "tasks.group.audio",
				Route: "tasks/audio/audio_ai_analysis",
				Family: "audio_processing",
				SourceMappings: []SourceMapping{
					{Kind: "audio_ai_analysis_task"},
				},
				Columns:      sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"ai", "audio", "summary"}),
				Available:    false,
			},
		},
	})

	// Document Processing group (Phase 5 types)
	groups = append(groups, TaskGroup{
		Label:      "tasks.group.document",
		Selectable: false,
		Types: []TaskSpec{
			{
				Type:  "document_convert",
				Group: "tasks.group.document",
				Route: "tasks/document/document_convert",
				Family: "document_processing",
				SourceMappings: []SourceMapping{
					{Kind: "document_convert_task"},
				},
				Columns:      sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"conversion", "document", "pdf"}),
				Available:    false,
			},
			{
				Type:  "document_fulltext",
				Group: "tasks.group.document",
				Route: "tasks/document/document_fulltext",
				Family: "document_processing",
				SourceMappings: []SourceMapping{
					{Kind: "document_fulltext_task"},
				},
				Columns:      sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"document", "ocr", "text"}),
				Available:    false,
			},
		},
	})

	// System group
	groups = append(groups, TaskGroup{
		Label:      "tasks.group.system",
		Selectable: false,
		Types: []TaskSpec{
			{
				Type:  "scan",
				Group: "tasks.group.system",
				Route: "tasks/system/scan",
				Family: "system",
				SourceMappings: []SourceMapping{
					{Kind: "scan_task"},
				},
				Columns: sortedColumns(append(commonTaskColumns(),
					ColumnSpec{Key: "added_count", Label: "tasks.column.added_count"},
					ColumnSpec{Key: "failed_count", Label: "tasks.column.failed_count"},
					ColumnSpec{Key: "finished_at", Label: "tasks.column.finished_at"},
					ColumnSpec{Key: "processed_count", Label: "tasks.column.processed_count"},
					ColumnSpec{Key: "source", Label: "tasks.column.source"},
					ColumnSpec{Key: "started_at", Label: "tasks.column.started_at"},
					ColumnSpec{Key: "total_count", Label: "tasks.column.total_count"},
				)),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"file_system", "scan"}),
				Available:    true,
			},
			{
				Type:  "scheduled",
				Group: "tasks.group.system",
				Route: "tasks/system/scheduled",
				Family: "system",
				SourceMappings: []SourceMapping{
					{Kind: "scheduled_task"},
				},
				Columns: sortedColumns(append(commonTaskColumns(),
					ColumnSpec{Key: "category", Label: "tasks.column.category"},
					ColumnSpec{Key: "enabled", Label: "tasks.column.enabled"},
					ColumnSpec{Key: "interval_min", Label: "tasks.column.interval_min"},
					ColumnSpec{Key: "last_run_at", Label: "tasks.column.last_run_at"},
					ColumnSpec{Key: "last_status", Label: "tasks.column.last_status"},
					ColumnSpec{Key: "name", Label: "tasks.column.name"},
					ColumnSpec{Key: "task_type", Label: "tasks.column.task_type"},
				)),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"scheduled"}),
				Available:    true,
			},
			{
				Type:  "subtitle",
				Group: "tasks.group.system",
				Route: "tasks/system/subtitle",
				Family: "system",
				SourceMappings: []SourceMapping{
					{Kind: "subtitle_task"},
				},
				Columns: sortedColumns(commonTaskColumns()),
				Filters:      commonTaskFilters(),
				Capabilities: sortedStrings([]string{"subtitle"}),
				Available:    true,
			},
		},
	})

	return &Registry{Groups: groups}
}
