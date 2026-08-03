import { api } from "./client";


// =============================================================================
// Shared Types — mirrors internal/taskcontrol Go types
// =============================================================================

export type NormalizedStatus = "waiting" | "running" | "done" | "failed" | "cancelled" | "skipped";

export const ALL_NORMALIZED_STATUSES: readonly NormalizedStatus[] = [
  "waiting", "running", "done", "failed", "cancelled", "skipped",
];

export interface OwnerLeaseInfo {
  owner: string;
  lease_until?: string;
}

export interface AdmissionInfo {
  runnable: boolean;
  blocker?: string;
  details?: Record<string, unknown>;
}

export interface DependencyInfo {
  task_identity: string;
  task_type: string;
  status: NormalizedStatus;
  required: boolean;
}

export interface EvidenceEntry {
  at: string;
  code: string;
  message: string;
  attempt: number;
}

export interface ProjectionRow {
  task_id: string;
  source_kind: string;
  source_id: number;
  task_type: string;
  family: string;
  normalized_status: NormalizedStatus;
  raw_status: string;
  revision: number;
  generation: number;
  retry_round: number;
  attempt: number;
  max_attempts: number;
  base_priority: number;
  effective_priority: number;
  available_at?: string;
  created_at: string;
  updated_at: string;
  media_id?: number;
  library_id?: number;
  admission?: AdmissionInfo;
  owner_lease?: OwnerLeaseInfo;
  terminal_reason?: string;
  tombstone: boolean;
  removed_at?: string;
  removed_by?: string;
  remove_reason?: string;
  dependencies?: DependencyInfo[];
  resources?: string[];
  projection_error?: string;
}

export interface SourceMapping {
  kind: string;
  internal_type?: string;
}

export interface ColumnSpec {
  key: string;
  label: string;
}

export interface FilterSpec {
  key: string;
  label: string;
  values?: string[];
}

export interface TaskSpec {
  type: string;
  group: string;
  route: string;
  family: string;
  source_mappings: SourceMapping[];
  columns: ColumnSpec[];
  filters: FilterSpec[];
  capabilities: string[];
  available: boolean;
}

export interface TaskGroup {
  label: string;
  selectable: boolean;
  types: TaskSpec[];
}

export interface Registry {
  groups: TaskGroup[];
}

export interface ListResult {
  items: ProjectionRow[];
  total: number;
  next_cursor?: string;
  has_more: boolean;
  truncated: boolean;
  snapshot_revision: number;
}

export interface AttemptInfo {
  attempt: number;
  status: string;
  error?: string;
  owner?: string;
  duration_secs?: number;
}

export interface AuditEntryInfo {
  id: number;
  action: string;
  actor_name: string;
  reason?: string;
  prev_status?: string;
  new_status?: string;
  created_at: string;
}

export interface DetailResult {
  row: ProjectionRow;
  attempts?: AttemptInfo[];
  dependencies?: DependencyInfo[];
  evidence?: EvidenceEntry[];
  audit_events?: AuditEntryInfo[];
}

export interface OverviewStatusCounts {
  waiting: number;
  running: number;
  done: number;
  failed: number;
  cancelled: number;
  skipped: number;
}

export interface OverviewSection {
  label: string;
  items: ProjectionRow[];
}

export interface ResourceBudget {
  kind: string;
  used: number;
  limit: number;
}

export interface RecentOperation {
  id: number;
  action: string;
  task_type?: string;
  actor_name: string;
  reason?: string;
  created_at: string;
}

export interface Overview {
  status_counts: OverviewStatusCounts;
  type_counts: Record<string, number>;
  running: OverviewSection;
  oldest: OverviewSection;
  blocked: OverviewSection;
  no_worker: OverviewSection;
  expired: OverviewSection;
  recovery: OverviewSection;
  cleanup: OverviewSection;
  resource_budgets?: ResourceBudget[];
  recent_ops?: RecentOperation[];
  snapshot_revision: number;
}

export interface AllowedActions {
  abort: boolean;
  remove: boolean;
  reset: boolean;
  run_now: boolean;
  skip: boolean;
  reopen: boolean;
}

export interface BatchItem {
  task_identity: string;
  expected_revision?: number;
  expected_generation?: number;
  expected_retry_round?: number;
}

export interface BatchResultItem {
  task_identity: string;
  success: boolean;
  error?: string;
  row?: ProjectionRow;
}

export interface BatchResult {
  operation_id: string;
  action: string;
  results: BatchResultItem[];
  total: number;
  succeeded: number;
  failed: number;
  retryable: BatchItem[];
}

// =============================================================================
// Runtime Guards
// =============================================================================

function isString(v: unknown): v is string {
  return typeof v === "string";
}

function isNumber(v: unknown): v is number {
  return typeof v === "number";
}

function isBoolean(v: unknown): v is boolean {
  return typeof v === "boolean";
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function isNormalizedStatus(v: unknown): v is NormalizedStatus {
  return isString(v) && ALL_NORMALIZED_STATUSES.includes(v as NormalizedStatus);
}

export function isProjectionRow(v: unknown): v is ProjectionRow {
  if (!isObject(v)) return false;
  return (
    isString(v.task_id) &&
    isString(v.source_kind) &&
    isNumber(v.source_id) &&
    isString(v.task_type) &&
    isString(v.family) &&
    isNormalizedStatus(v.normalized_status) &&
    isString(v.raw_status) &&
    isNumber(v.revision) &&
    isNumber(v.generation) &&
    isNumber(v.retry_round) &&
    isNumber(v.attempt) &&
    isNumber(v.max_attempts) &&
    isNumber(v.base_priority) &&
    isNumber(v.effective_priority) &&
    isString(v.created_at) &&
    isString(v.updated_at) &&
    isBoolean(v.tombstone)
  );
}

export function isRegistry(v: unknown): v is Registry {
  if (!isObject(v)) return false;
  if (!Array.isArray(v.groups)) return false;
  return true;
}

export function isListResult(v: unknown): v is ListResult {
  if (!isObject(v)) return false;
  return (
    Array.isArray(v.items) &&
    isNumber(v.total) &&
    isBoolean(v.has_more) &&
    isBoolean(v.truncated) &&
    isNumber(v.snapshot_revision)
  );
}

export function isDetailResult(v: unknown): v is DetailResult {
  if (!isObject(v)) return false;
  if (!isProjectionRow(v.row)) return false;
  return true;
}

export function isOverview(v: unknown): v is Overview {
  if (!isObject(v)) return false;
  if (!isObject(v.status_counts)) return false;
  if (!isObject(v.type_counts)) return false;
  if (!isObject(v.running) || !isObject(v.oldest) || !isObject(v.blocked)) return false;
  if (!isObject(v.no_worker) || !isObject(v.expired) || !isObject(v.recovery) || !isObject(v.cleanup)) return false;
  if (!isNumber(v.snapshot_revision)) return false;
  const sc = v.status_counts;
  return (
    isNumber(sc.waiting) && isNumber(sc.running) && isNumber(sc.done) &&
    isNumber(sc.failed) && isNumber(sc.cancelled) && isNumber(sc.skipped)
  );
}

export function isAllowedActions(v: unknown): v is AllowedActions {
  if (!isObject(v)) return false;
  return (
    isBoolean(v.abort) &&
    isBoolean(v.remove) &&
    isBoolean(v.reset) &&
    isBoolean(v.run_now) &&
    isBoolean(v.skip) &&
    isBoolean(v.reopen)
  );
}

export function isBatchResult(v: unknown): v is BatchResult {
  if (!isObject(v)) return false;
  return (
    isString(v.operation_id) &&
    isString(v.action) &&
    Array.isArray(v.results) &&
    isNumber(v.total) &&
    isNumber(v.succeeded) &&
    isNumber(v.failed) &&
    Array.isArray(v.retryable)
  );
}

// =============================================================================
// API Parameter Types
// =============================================================================

export interface TaskControlListParams {
  task_type?: string;
  status?: string;
  source?: string;
  library_id?: number;
  generation?: number;
  capability?: string;
  owner?: string;
  blocker?: string;
  removed?: string;
  cursor?: string;
  limit?: number;
}

export interface TaskControlActionParams {
  action: string;
  reason: string;
  expected_revision?: number;
  expected_generation?: number;
  expected_retry_round?: number;
}

export interface TaskControlBatchParams {
  operation_id: string;
  action: string;
  reason: string;
  items: BatchItem[];
}

// =============================================================================
// API Client Functions
// =============================================================================

export async function fetchTaskControlRegistry(): Promise<Registry> {
  const { data } = await api.get<Registry>("/api/v1/task-control/registry");
  return data;
}

export async function fetchTaskControlOverview(signal?: AbortSignal): Promise<Overview> {
  const { data } = await api.get<Overview>("/api/v1/task-control/overview", { signal });
  return data;
}

export async function fetchTaskControlList(params: TaskControlListParams): Promise<ListResult> {
  const cleanParams: Record<string, string | number> = {};
  for (const [key, val] of Object.entries(params)) {
    if (val !== undefined && val !== "") {
      cleanParams[key] = val;
    }
  }
  const { data } = await api.get<ListResult>("/api/v1/task-control/list", { params: cleanParams });
  return data;
}

export async function fetchTaskControlDetail(taskId: string): Promise<DetailResult | null> {
  try {
    const { data } = await api.get<DetailResult>(`/api/v1/task-control/${encodeURIComponent(taskId)}/detail`);
    return data;
  } catch (err) {
    const ax = err as { response?: { status?: number } };
    if (ax.response?.status === 404) return null;
    throw err;
  }
}

export async function fetchTaskControlActions(
  taskId: string,
  params: TaskControlActionParams,
): Promise<{ status: string; action: string; task_id: string; row?: ProjectionRow }> {
  const { data } = await api.post(
    `/api/v1/task-control/${encodeURIComponent(taskId)}/actions`,
    {
      action: params.action,
      reason: params.reason,
      expected_revision: params.expected_revision,
      expected_generation: params.expected_generation,
      expected_retry_round: params.expected_retry_round,
    },
  );
  return data;
}

export async function fetchTaskControlBatch(params: TaskControlBatchParams): Promise<BatchResult> {
  const { data } = await api.post("/api/v1/task-control/batch", params);
  return data;
}
