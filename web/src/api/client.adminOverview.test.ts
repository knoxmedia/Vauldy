import { describe, expect, it, vi } from "vitest";
import { api, fetchAdminOverview, type AdminOverview, type PublicationPolicyDiagnostic } from "./client";
import { isAdminOverview } from "../pages/AdminConsole";

const baseOverview = (): AdminOverview => ({
  monitor: { cpu_percent: 1, memory_percent: 2, disk_percent: 3, transcode_task_count: 0, media_total: 1 },
  system: { cpu_count: 4, memory_total: 8, os: "test", database: "sqlite", software_version: "dev" },
  activities: [{ id: 1, username: "a", action: "scan", media_id: 1, message: "m", created_at: "2026-07-22T00:00:00Z" }],
  post_ingest_queue: { by_status: { waiting: 1 }, by_type: { poster: { waiting: 1 } }, oldest_waiting_seconds: 1, expired_lease_count: 0 },
  running_post_ingest_tasks: [],
  scan_leases: [],
  resource_budget: { global_limit: 1, global_used: 0, poster_limit: 1, poster_used: 0, preview_limit: 1, preview_used: 0 },
  sqlite_metrics: { scope: "process_since_start", persistent: false, busy_retries: 0, busy_exhausted: 0, progress_batches: 0, log_batches: 0, log_failures: 0, dropped_logs: 0 },
  publication_policy: [],
});

describe("fetchAdminOverview", () => {
  it("passes the caller signal and a 3000ms client timeout", async () => {
    const controller = new AbortController();
    const getSpy = vi.spyOn(api, "get").mockResolvedValue({ data: { monitor: {}, system: {}, activities: [] } });

    await fetchAdminOverview(controller.signal);

    expect(getSpy).toHaveBeenCalledWith("/api/v1/admin/overview", { signal: controller.signal, timeout: 3000 });
    getSpy.mockRestore();
  });

  it("types and accepts publication_policy diagnostics on AdminOverview", () => {
    const row: PublicationPolicyDiagnostic = {
      media_id: 10,
      run_id: 100,
      generation: 1,
      policy_version: 2,
      status: "failed",
      terminal_reason: "required_failed",
      required_waiting: 0,
      required_failed: 1,
      optional_waiting: 1,
      optional_failed: 0,
      adapter_unavailable: ["scrape"],
      metadata_errors: ["ffprobe: duration unavailable"],
      recovery_error: "asset recovery failed",
    };
    const overview = { ...baseOverview(), publication_policy: [row] };
    expect(isAdminOverview(overview)).toBe(true);
    expect(overview.publication_policy[0]?.adapter_unavailable).toEqual(["scrape"]);
  });

  it("rejects overviews missing or malformed publication_policy", () => {
    const { publication_policy: _omit, ...without } = baseOverview();
    expect(isAdminOverview(without)).toBe(false);
    expect(isAdminOverview({ ...baseOverview(), publication_policy: [{ media_id: "x" }] })).toBe(false);
  });
});
