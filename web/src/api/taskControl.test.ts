import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  fetchTaskControlRegistry,
  fetchTaskControlOverview,
  fetchTaskControlList,
  fetchTaskControlDetail,
  fetchTaskControlActions,
  fetchTaskControlBatch,
  isProjectionRow,
  isRegistry,
  isListResult,
  isDetailResult,
  isOverview,
  isAllowedActions,
  isBatchResult,
} from "./taskControl";
import type {
  ProjectionRow,
  Registry,
  DetailResult,
} from "./taskControl";

// Mock the client module's api instance
const mockGet = vi.fn();
const mockPost = vi.fn();

vi.mock("./client", () => ({
  api: {
    get: (...args: unknown[]) => mockGet(...args),
    post: (...args: unknown[]) => mockPost(...args),
  },
}));

function makeRow(overrides: Partial<ProjectionRow> = {}): ProjectionRow {
  return {
    task_id: "orchestration:1",
    source_kind: "orchestration",
    source_id: 1,
    task_type: "poster",
    family: "media_ingestion",
    normalized_status: "waiting",
    raw_status: "waiting",
    revision: 1,
    generation: 1,
    retry_round: 0,
    attempt: 0,
    max_attempts: 3,
    base_priority: 0,
    effective_priority: 0,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    tombstone: false,
    allowed_actions: { abort: false, remove: false, reset: false, run_now: false, skip: false, reopen: false },
    ...overrides,
  };
}

describe("taskControl API client", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("isProjectionRow", () => {
    it("rejects null/undefined", () => {
      expect(isProjectionRow(null)).toBe(false);
      expect(isProjectionRow(undefined)).toBe(false);
      expect(isProjectionRow(42)).toBe(false);
      expect(isProjectionRow("string")).toBe(false);
      expect(isProjectionRow([])).toBe(false);
    });

    it("requires string task_id", () => {
      expect(isProjectionRow({ ...makeRow(), task_id: 42 })).toBe(false);
    });

    it("requires valid normalized_status", () => {
      expect(isProjectionRow({ ...makeRow(), normalized_status: "unknown" })).toBe(false);
    });

    it("accepts valid rows", () => {
      expect(isProjectionRow(makeRow())).toBe(true);
    });

    it("accepts nullable timestamps", () => {
      expect(isProjectionRow(makeRow({ available_at: undefined }))).toBe(true);
      expect(isProjectionRow(makeRow({ removed_at: undefined }))).toBe(true);
      expect(isProjectionRow(makeRow({ available_at: "2025-01-01T00:00:00Z" }))).toBe(true);
    });
  });

  describe("isRegistry", () => {
    it("rejects non-objects", () => {
      expect(isRegistry(null)).toBe(false);
      expect(isRegistry([])).toBe(false);
    });

    it("requires groups array", () => {
      expect(isRegistry({ groups: "not-array" })).toBe(false);
    });

    it("accepts valid registry", () => {
      expect(isRegistry({
        groups: [{ label: "tasks.group.overview", selectable: false, types: [] }],
      })).toBe(true);
    });
  });

  describe("isListResult", () => {
    it("validates list result shape", () => {
      expect(isListResult(null)).toBe(false);
      expect(isListResult({})).toBe(false);
      expect(isListResult({
        items: [makeRow()],
        total: 1,
        has_more: false,
        truncated: false,
        snapshot_revision: 1,
      })).toBe(true);
    });

    it("allows optional next_cursor", () => {
      expect(isListResult({
        items: [],
        total: 0,
        next_cursor: "abc123",
        has_more: false,
        truncated: false,
        snapshot_revision: 1,
      })).toBe(true);
    });
  });

  describe("isDetailResult", () => {
    it("validates detail result shape", () => {
      expect(isDetailResult({ row: makeRow() })).toBe(true);
    });

    it("allows optional arrays", () => {
      expect(isDetailResult({
        row: makeRow(),
        attempts: [],
        dependencies: [],
        evidence: [],
        audit_events: [],
      })).toBe(true);
    });
  });

  describe("isOverview", () => {
    it("validates overview shape", () => {
      expect(isOverview({
        status_counts: { waiting: 1, running: 0, done: 0, failed: 0, cancelled: 0, skipped: 0 },
        type_counts: {},
        running: { label: "running", items: [] },
        oldest: { label: "oldest", items: [] },
        blocked: { label: "blocked", items: [] },
        no_worker: { label: "no_worker", items: [] },
        expired: { label: "expired", items: [] },
        recovery: { label: "recovery", items: [] },
        cleanup: { label: "cleanup", items: [] },
        snapshot_revision: 1,
      })).toBe(true);
    });
  });

  describe("isAllowedActions", () => {
    it("validates allowed actions shape", () => {
      expect(isAllowedActions({
        abort: true, remove: true, reset: false, run_now: false, skip: false, reopen: false,
      })).toBe(true);
    });

    it("rejects non-objects", () => {
      expect(isAllowedActions(null)).toBe(false);
      expect(isAllowedActions({ abort: "yes" })).toBe(false);
    });
  });

  describe("isBatchResult", () => {
    it("validates batch result shape", () => {
      expect(isBatchResult({
        operation_id: "uuid-1", action: "abort", results: [],
        total: 0, succeeded: 0, failed: 0, retryable: [],
      })).toBe(true);
    });
  });

  describe("fetchTaskControlRegistry", () => {
    it("returns parsed registry", async () => {
      const reg: Registry = {
        groups: [{ label: "tasks.group.overview", selectable: false, types: [] }],
      };
      mockGet.mockResolvedValueOnce({ data: reg });
      const result = await fetchTaskControlRegistry();
      expect(result).toEqual(reg);
      expect(mockGet).toHaveBeenCalledWith("/api/v1/admin/tasks/registry");
    });
  });

  describe("fetchTaskControlOverview", () => {
    it("returns parsed overview", async () => {
      const ov = {
        status_counts: { waiting: 3, running: 1, done: 0, failed: 0, cancelled: 0, skipped: 0 },
        type_counts: { poster: 4 },
        running: { label: "running", items: [] },
        oldest: { label: "oldest", items: [] },
        blocked: { label: "blocked", items: [] },
        no_worker: { label: "no_worker", items: [] },
        expired: { label: "expired", items: [] },
        recovery: { label: "recovery", items: [] },
        cleanup: { label: "cleanup", items: [] },
        snapshot_revision: 1,
      };
      mockGet.mockResolvedValueOnce({ data: ov });
      const result = await fetchTaskControlOverview();
      expect(result).toEqual(ov);
    });

    it("supports abort signal", async () => {
      const ctrl = new AbortController();
      mockGet.mockResolvedValueOnce({ data: {} });
      await fetchTaskControlOverview(ctrl.signal);
      expect(mockGet.mock.calls[0]![1]).toHaveProperty("signal", ctrl.signal);
    });
  });

  describe("fetchTaskControlList", () => {
    it("maps all list params", async () => {
      mockGet.mockResolvedValueOnce({
        data: { items: [], total: 0, has_more: false, truncated: false, snapshot_revision: 1 },
      });
      await fetchTaskControlList({
        task_type: "poster", status: "running", cursor: "abc", limit: 25, removed: "exclude",
      });
      expect(mockGet).toHaveBeenCalledWith("/api/v1/admin/tasks", {
        params: { task_type: "poster", status: "running", cursor: "abc", limit: 25, removed: "exclude" },
      });
    });

    it("accepts library_id and generation", async () => {
      mockGet.mockResolvedValueOnce({
        data: { items: [], total: 0, has_more: false, truncated: false, snapshot_revision: 1 },
      });
      await fetchTaskControlList({ task_type: "transcode", library_id: 42, generation: 3 });
      expect(mockGet).toHaveBeenCalledWith("/api/v1/admin/tasks", {
        params: { task_type: "transcode", library_id: 42, generation: 3 },
      });
    });
  });

  describe("fetchTaskControlDetail", () => {
    it("returns detail by task_id", async () => {
      const detail: DetailResult = { row: makeRow() };
      mockGet.mockResolvedValueOnce({ data: detail });
      const result = await fetchTaskControlDetail("orchestration:1");
      expect(result).toEqual(detail);
      expect(mockGet).toHaveBeenCalledWith("/api/v1/admin/tasks/orchestration%3A1");
    });
  });

  describe("fetchTaskControlActions", () => {
    it("sends action payload", async () => {
      mockPost.mockResolvedValueOnce({
        data: { status: "ok", action: "abort", task_id: "orchestration:1", row: makeRow() },
      });
      const result = await fetchTaskControlActions("orchestration:1", {
        action: "abort", reason: "test abort", expected_revision: 5,
      });
      expect(result.row).toBeDefined();
      expect(mockPost).toHaveBeenCalledWith(
        "/api/v1/admin/tasks/orchestration%3A1/actions",
        expect.objectContaining({ action: "abort", reason: "test abort", expected_revision: 5 }),
      );
    });
  });

  describe("fetchTaskControlBatch", () => {
    it("sends batch payload", async () => {
      mockPost.mockResolvedValueOnce({
        data: { operation_id: "op-1", action: "remove", results: [], total: 1, succeeded: 1, failed: 0, retryable: [] },
      });
      const result = await fetchTaskControlBatch({
        operation_id: "op-1", action: "remove", reason: "cleanup",
        items: [{ task_identity: "orchestration:1" }],
      });
      expect(result.operation_id).toBe("op-1");
      expect(mockPost).toHaveBeenCalledWith("/api/v1/admin/tasks/batch", {
        operation_id: "op-1", action: "remove", reason: "cleanup",
        items: [{ task_identity: "orchestration:1" }],
      });
    });
  });
});
