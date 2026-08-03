import { describe, expect, it, vi } from "vitest";
import { api, fetchAdminOverview, type AdminOverview } from "./client";
import { isAdminOverviewSystemOnly } from "../pages/AdminConsole";

const baseOverview = (): AdminOverview => ({
  monitor: { cpu_percent: 1, memory_percent: 2, disk_percent: 3 },
  system: { cpu_count: 4, memory_total: 8, os: "test", database: "sqlite", software_version: "dev" },
  activities: [{ id: 1, username: "a", action: "scan", media_id: 1, message: "m", created_at: "2026-07-22T00:00:00Z" }],
  sqlite_metrics: { scope: "process_since_start", persistent: false, busy_retries: 0, busy_exhausted: 0, progress_batches: 0, log_batches: 0, log_failures: 0, dropped_logs: 0 },
});

describe("fetchAdminOverview", () => {
  it("passes the caller signal and a 3000ms client timeout", async () => {
    const controller = new AbortController();
    const getSpy = vi.spyOn(api, "get").mockResolvedValue({ data: { monitor: {}, system: {}, activities: [] } });

    await fetchAdminOverview(controller.signal);

    expect(getSpy).toHaveBeenCalledWith("/api/v1/admin/overview", { signal: controller.signal, timeout: 3000 });
    getSpy.mockRestore();
  });

  it("accepts system-only overview with valid monitor, system, activities, sqlite_metrics", () => {
    const overview = baseOverview();
    expect(isAdminOverviewSystemOnly(overview)).toBe(true);
  });

  it("rejects overviews that contain task control fields", () => {
    // Adding any task control field must cause rejection
    const withPub = { ...baseOverview(), publication_policy: [] };
    expect(isAdminOverviewSystemOnly(withPub)).toBe(false);

    const withQueue = { ...baseOverview(), post_ingest_queue: {} };
    expect(isAdminOverviewSystemOnly(withQueue)).toBe(false);

    const withAlignment = { ...baseOverview(), task_alignment: {} };
    expect(isAdminOverviewSystemOnly(withAlignment)).toBe(false);

    const withBudget = { ...baseOverview(), resource_budget: {} };
    expect(isAdminOverviewSystemOnly(withBudget)).toBe(false);

    const withMonitorField = { ...baseOverview(), monitor: { ...baseOverview().monitor, transcode_task_count: 1 } };
    expect(isAdminOverviewSystemOnly(withMonitorField)).toBe(false);
  });

  it("rejects overviews with missing required system fields", () => {
    // Missing monitor fields
    expect(isAdminOverviewSystemOnly({ ...baseOverview(), monitor: {} })).toBe(false);
    // Missing system fields
    expect(isAdminOverviewSystemOnly({ ...baseOverview(), system: null })).toBe(false);
    // Missing activities
    expect(isAdminOverviewSystemOnly({ ...baseOverview(), activities: null })).toBe(false);
    // Missing sqlite_metrics
    expect(isAdminOverviewSystemOnly({ ...baseOverview(), sqlite_metrics: null })).toBe(false);
  });
});
