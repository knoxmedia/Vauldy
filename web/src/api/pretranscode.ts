import { api } from "./client";

// --- Types (mirror internal/pretranscode structs) ---

export type Preset = {
  id: number;
  name: string;
  description?: string;
  output_format: "hls" | "mp4" | "dash" | "flv";
  encryption_mode: "none" | "aes128" | "powerdrm" | "drm";
  video_codec: string;
  video_preset?: string;
  video_crf?: number;
  video_maxrate?: string;
  video_bufsize?: string;
  video_profile?: string;
  video_gop?: number;
  video_pix_fmt?: string;
  audio_codec: "aac" | "mp3" | "copy";
  audio_bitrate: string;
  audio_channels?: number;
  audio_sample_rate?: number;
  hw_fallback: boolean;
  output_dir_mode?: "source" | "custom" | "data";
  output_dir_custom?: string;
  is_builtin: boolean;
  is_enabled: boolean;
  sort_order: number;
  created_at?: string;
  updated_at?: string;
  renditions?: Rendition[];
};

export type Rendition = {
  id?: number;
  preset_id?: number;
  name: string;
  height: number;
  width?: number;
  video_bitrate: string;
  audio_bitrate?: string;
  bandwidth?: number;
  sort_order?: number;
};

export type UnifiedTask = {
  id: number;
  task_type: "batch" | "pretranscode";
  file_id: string;
  media_id: number;
  title: string;
  quality: string;
  status: string;
  progress: number;
  error_message?: string;
  output_path?: string;
  preset_id?: number;
  preset_name?: string;
  priority?: string;
  encryption_mode?: string;
  output_format?: string;
  created_at: string;
  started_at?: string;
  completed_at?: string;
};

export type RenditionJob = {
  id: number;
  task_id: number;
  rendition_id: number;
  rendition_name: string;
  status: "waiting" | "running" | "done" | "failed" | "cancelled";
  progress: number;
  output_path?: string;
  error_message?: string;
  encoder_used?: string;
  started_at?: string;
  completed_at?: string;
};

export type Webhook = {
  id?: number;
  name: string;
  url: string;
  events: string[];
  headers?: Record<string, string>;
  secret?: string;
  is_enabled: boolean;
  created_at?: string;
  updated_at?: string;
};

export type WebhookLog = {
  id: number;
  webhook_id: number;
  event: string;
  payload?: string;
  response_code?: number;
  response_body?: string;
  error?: string;
  retry_count: number;
  created_at: string;
};

export type ClusterNode = {
  id: string;
  host: string;
  status: "online" | "offline";
  hardware_encoders?: string[];
  current_tasks: number;
  max_concurrent: number;
  cpu_usage?: number;
  gpu_usage?: number;
  last_heartbeat?: string;
};

export type StorageStats = {
  task_count: number;
  output_bytes: number;
  output_mb: number;
};

export type OptimizedRendition = {
  rendition_job_id: number;
  rendition_name: string;
  resolution: string;
  bitrate: string;
  output_format: string;
  preset_name: string;
  file_size: number;
  completed_at: string;
};

export type MediaOptimizationStatus = {
  media_id: number;
  filename: string;
  duration: number;
  resolution: string;
  file_size: number;
  optimization_available: boolean;
  optimized_renditions: OptimizedRendition[];
  running_tasks: Array<{
    task_id: number;
    preset_name: string;
    status: string;
    progress: number;
    error_message?: string;
  }>;
};

// --- Preset API ---

export async function fetchPresets() {
  const { data } = await api.get<{ items: Preset[] }>("/api/v1/admin/settings/pretranscode-presets");
  return data.items ?? [];
}

export async function fetchPreset(id: number) {
  const { data } = await api.get<Preset>(`/api/v1/admin/settings/pretranscode-presets/${id}`);
  return data;
}

export async function createPreset(payload: Omit<Preset, "id" | "is_builtin" | "is_enabled" | "sort_order" | "created_at" | "updated_at"> & { renditions: Rendition[] }) {
  const { data } = await api.post<Preset>("/api/v1/admin/settings/pretranscode-presets", payload);
  return data;
}

export async function updatePreset(id: number, payload: Parameters<typeof createPreset>[0]) {
  const { data } = await api.put<Preset>(`/api/v1/admin/settings/pretranscode-presets/${id}`, payload);
  return data;
}

export async function deletePreset(id: number) {
  await api.delete(`/api/v1/admin/settings/pretranscode-presets/${id}`);
}

export async function clonePreset(id: number, name: string) {
  const { data } = await api.post<Preset>(`/api/v1/admin/settings/pretranscode-presets/${id}/clone`, { name });
  return data;
}

export async function togglePreset(id: number) {
  const { data } = await api.put<{ ok: boolean; is_enabled: boolean }>(`/api/v1/admin/settings/pretranscode-presets/${id}/toggle`);
  return data;
}

export async function fetchRenditions(presetId: number) {
  const { data } = await api.get<{ items: Rendition[] }>(`/api/v1/admin/settings/pretranscode-presets/${presetId}/renditions`);
  return data.items ?? [];
}

export async function addRendition(presetId: number, rendition: Rendition) {
  const { data } = await api.post<Rendition>(`/api/v1/admin/settings/pretranscode-presets/${presetId}/renditions`, rendition);
  return data;
}

export async function updateRendition(presetId: number, renditionId: number, rendition: Rendition) {
  const { data } = await api.put<Rendition>(`/api/v1/admin/settings/pretranscode-presets/${presetId}/renditions/${renditionId}`, rendition);
  return data;
}

export async function deleteRendition(presetId: number, renditionId: number) {
  await api.delete(`/api/v1/admin/settings/pretranscode-presets/${presetId}/renditions/${renditionId}`);
}

export async function sortRenditions(presetId: number, orderedIds: number[]) {
  await api.put(`/api/v1/admin/settings/pretranscode-presets/${presetId}/renditions/sort`, { ordered_ids: orderedIds });
}

// --- Task API ---

export async function fetchUnifiedTasks(type: "all" | "batch" | "pretranscode" = "all", limit = 100) {
  const { data } = await api.get<{ items: UnifiedTask[] }>("/api/v1/admin/transcode/tasks", { params: { type, limit } });
  return data.items ?? [];
}

export async function createPretranscodeTask(mediaIds: number[], presetId: number, priority = "normal") {
  const { data } = await api.post<{ task_ids: number[] }>("/api/v1/admin/transcode/tasks", {
    media_ids: mediaIds,
    preset_id: presetId,
    priority,
  });
  return data;
}

export async function createBatchPretranscodeTask(libraryId: number, presetId: number, filter = "untranscoded", priority = "low") {
  const { data } = await api.post<{ created: number }>("/api/v1/admin/transcode/batch", {
    library_id: libraryId,
    preset_id: presetId,
    filter,
    priority,
  });
  return data;
}

export async function getPretranscodeTask(id: number) {
  const { data } = await api.get<{ task: UnifiedTask; renditions: RenditionJob[] }>(`/api/v1/admin/transcode/tasks/${id}`);
  return data;
}

export async function cancelPretranscodeTask(id: number) {
  await api.post(`/api/v1/admin/transcode/tasks/${id}/cancel`);
}

export async function retryPretranscodeTask(id: number) {
  await api.post(`/api/v1/admin/transcode/tasks/${id}/retry`);
}

export async function deletePretranscodeTask(id: number) {
  await api.delete(`/api/v1/admin/transcode/tasks/${id}`);
}

export async function pausePretranscodeTask(id: number) {
  await api.post(`/api/v1/admin/transcode/tasks/${id}/pause`);
}

export async function resumePretranscodeTask(id: number) {
  await api.post(`/api/v1/admin/transcode/tasks/${id}/resume`);
}

export async function fetchRenditionJobs(taskId: number) {
  const { data } = await api.get<{ items: RenditionJob[] }>(`/api/v1/admin/transcode/tasks/${taskId}/renditions`);
  return data.items ?? [];
}

export async function cancelRenditionJob(taskId: number, jobId: number) {
  await api.post(`/api/v1/admin/transcode/tasks/${taskId}/renditions/${jobId}/cancel`);
}

export async function retryRenditionJob(taskId: number, jobId: number) {
  await api.post(`/api/v1/admin/transcode/tasks/${taskId}/renditions/${jobId}/retry`);
}

export async function cleanupFailedPretranscodeTasks(days = 7) {
  const { data } = await api.post<{ deleted: number }>("/api/v1/admin/transcode/cleanup-failed", { days });
  return data.deleted;
}

export async function fetchPretranscodeStorage() {
  const { data } = await api.get<StorageStats>("/api/v1/admin/transcode/storage");
  return data;
}

export async function cleanupPretranscodeOutputs(fileId: string) {
  await api.post("/api/v1/admin/transcode/cleanup", { file_id: fileId });
}

// --- Media Optimization API ---

export async function fetchMediaOptimizationStatus(mediaId: number) {
  const { data } = await api.get<MediaOptimizationStatus>(`/api/v1/media/${mediaId}/optimization`);
  return data;
}

export async function createOptimizationTask(mediaId: number, presetId: number, excludeExisting = true) {
  const { data } = await api.post<{ task_id: number }>(`/api/v1/media/${mediaId}/optimization`, {
    preset_id: presetId,
    exclude_existing: excludeExisting,
  });
  return data;
}

export async function removeOptimizationRendition(mediaId: number, renditionJobId: number) {
  await api.delete(`/api/v1/media/${mediaId}/optimization/renditions/${renditionJobId}`);
}

export async function batchRemoveOptimizationRenditions(mediaId: number, renditionJobIds: number[]) {
  await api.delete(`/api/v1/media/${mediaId}/optimization/renditions`, {
    data: { rendition_job_ids: renditionJobIds },
  });
}

// --- Webhook API ---

export async function fetchWebhooks() {
  const { data } = await api.get<{ items: Webhook[] }>("/api/v1/admin/settings/pretranscode-webhooks");
  return data.items ?? [];
}

export async function createWebhook(webhook: Webhook) {
  const { data } = await api.post<Webhook>("/api/v1/admin/settings/pretranscode-webhooks", webhook);
  return data;
}

export async function updateWebhook(id: number, webhook: Webhook) {
  const { data } = await api.put<Webhook>(`/api/v1/admin/settings/pretranscode-webhooks/${id}`, webhook);
  return data;
}

export async function deleteWebhook(id: number) {
  await api.delete(`/api/v1/admin/settings/pretranscode-webhooks/${id}`);
}

export async function testWebhook(id: number) {
  await api.post(`/api/v1/admin/settings/pretranscode-webhooks/${id}/test`);
}

export async function fetchWebhookLogs(id: number, limit = 100) {
  const { data } = await api.get<{ items: WebhookLog[] }>(`/api/v1/admin/settings/pretranscode-webhooks/${id}/logs`, { params: { limit } });
  return data.items ?? [];
}

// --- Cluster API ---

export async function fetchClusterNodes() {
  const { data } = await api.get<{ nodes: ClusterNode[]; queue_depth: number; total_active_tasks: number }>(
    "/api/v1/admin/transcode/cluster/nodes",
  );
  return data;
}

export async function fetchClusterStats() {
  const { data } = await api.get<{ nodes: number; online: number; queue_depth: number; active_tasks: number; mode: string }>(
    "/api/v1/admin/transcode/cluster/stats",
  );
  return data;
}

// --- Playback info ---

export async function fetchPretranscodeInfo(mediaId: number) {
  const { data } = await api.get<{
    available: boolean;
    preset_name?: string;
    renditions?: Array<{ name: string; status: string; progress: number }>;
    encryption?: string;
    output_format?: string;
  }>(`/api/v1/media/${mediaId}/pretranscode/info`);
  return data;
}

// --- Available encoders ---

export type EncoderInfo = {
  id: string;
  name: string;
  family: "h264" | "h265";
  type: "software" | "hardware";
};

export async function fetchAvailableEncoders() {
  const { data } = await api.get<{ encoders: EncoderInfo[] }>(
    "/api/v1/admin/transcode/encoders",
  );
  return data.encoders;
}
