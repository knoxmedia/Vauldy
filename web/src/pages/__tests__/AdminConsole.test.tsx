import React from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminOverview } from "../../api/client";
import en from "../../i18n/locales/en.json";
import zhCN from "../../i18n/locales/zh-CN.json";
import zhTW from "../../i18n/locales/zh-TW.json";

const { fetchAdminOverviewMock, authState } = vi.hoisted(() => ({
  fetchAdminOverviewMock: vi.fn(),
  authState: { token: "admin-token" },
}));

vi.mock("../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/client")>();
  return { ...actual, fetchAdminOverview: fetchAdminOverviewMock };
});

vi.mock("../../i18n", () => ({ useT: () => (key: string) => key }));
vi.mock("../../store/auth", () => ({
  useAuthStore: (selector: (state: { token: string }) => unknown) => selector(authState),
}));
vi.mock("../../lib/datetime", () => ({ renderServerDateTime: (value: string) => value }));
vi.mock("@ant-design/icons", () => ({ ReloadOutlined: () => null }));
vi.mock("antd", () => {
  const Wrapper = ({ children }: React.PropsWithChildren) => <div>{children}</div>;
  return {
    Alert: ({ message, action }: { message: React.ReactNode; action?: React.ReactNode }) => <div role="alert">{message}{action}</div>,
    Button: ({ children, onClick, "aria-label": ariaLabel }: React.ButtonHTMLAttributes<HTMLButtonElement>) => <button aria-label={ariaLabel} onClick={onClick}>{children}</button>,
    Card: ({ children, loading, title, extra }: React.PropsWithChildren<{ loading?: boolean; title?: React.ReactNode; extra?: React.ReactNode }>) => <section><div>{title}</div>{extra}{loading ? <div role="status">loading</div> : children}</section>,
    Col: Wrapper,
    Empty: Object.assign(() => <div>empty</div>, { PRESENTED_IMAGE_SIMPLE: null }),
    Progress: ({ percent }: { percent: number }) => <span>{percent}%</span>,
    Row: Wrapper,
    Space: Wrapper,
    Statistic: ({ title, value, suffix }: { title: React.ReactNode; value: React.ReactNode; suffix?: React.ReactNode }) => <div><span>{title}</span><span>{String(value)}{suffix}</span></div>,
    Table: ({ dataSource = [], columns = [], rowKey }: { dataSource?: Array<Record<string, unknown>>; columns?: Array<{ dataIndex?: string; render?: (value: unknown, row: Record<string, unknown>) => React.ReactNode }>; rowKey?: string | ((row: Record<string, unknown>) => React.Key) }) => {
      const tableName = dataSource.some((row) => "adapter_unavailable" in row) ? "publication-policy-table"
        : dataSource.some((row) => "lease_owner" in row) ? "running-table"
          : dataSource.some((row) => "owner_id" in row) ? "lease-table"
            : "other-table";
      return <div data-testid={tableName} data-row-key={typeof rowKey === "string" ? rowKey : "function"}>{dataSource.map((row, index) => <div key={index}>{columns.map((column, columnIndex) => <span key={columnIndex}>{column.render ? column.render(column.dataIndex ? row[column.dataIndex] : undefined, row) : String(column.dataIndex ? row[column.dataIndex] ?? "" : "")}</span>)}</div>)}</div>;
    },
    Tag: ({ children }: React.PropsWithChildren) => <span>{children}</span>,
    Tooltip: Wrapper,
  };
});


import AdminConsolePage from "../AdminConsole";

class MockEventSource {
  static instances: MockEventSource[] = [];
  readonly listeners = new Map<string, (event: MessageEvent) => void>();
  onerror: ((event: Event) => void) | null = null;
  close = vi.fn();

  constructor(readonly url: string) {
    MockEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    this.listeners.set(type, listener as (event: MessageEvent) => void);
  }

  emitOverview(data: AdminOverview) {
    this.emitRaw(JSON.stringify(data));
  }

  emitRaw(data: string) {
    this.listeners.get("overview")?.(new MessageEvent("overview", { data }));
  }

  emitError() {
    this.onerror?.(new Event("error"));
  }
}

const overview = (cpu: number, message: string): AdminOverview => ({
  monitor: { cpu_percent: cpu, memory_percent: 20, disk_percent: 30, transcode_task_count: 4, media_total: 5 },
  system: { cpu_count: 8, memory_total: 16 * 1024 ** 3, os: `os-${cpu}`, database: "sqlite", software_version: "test" },
  activities: [{ id: cpu, username: "admin", action: "scan", media_id: 1, message, created_at: "2026-07-17T00:00:00Z" }],
  post_ingest_queue: {
    by_status: { failed: 2, running: 1, waiting: 3 },
    by_type: { poster: { failed: 2, running: 1 }, preview: { waiting: 99 } },
    oldest_waiting_seconds: 127,
    expired_lease_count: 4,
  },
  task_alignment: {
    by_type: {
      subtitle: { waiting: 0, running: 0, done: 0, failed: 0, cancelled: 0 },
      preview: { waiting: 3, running: 0, done: 0, failed: 0, cancelled: 0 },
      atrack: { waiting: 0, running: 0, done: 0, failed: 0, cancelled: 0 },
      keyframe: { waiting: 0, running: 0, done: 0, failed: 0, cancelled: 0 },
      encrypt: { waiting: 0, running: 0, done: 0, failed: 0, cancelled: 0 },
    },
  },
  running_post_ingest_tasks: [{ id: 901, media_id: 44, task_type: "poster", type: "poster", scan_task_id: 71, attempts: 2, attempt: 2, max_attempts: 5, run_seconds: 33, started_at: "2026-07-17T00:00:00Z", lease_owner: "poster-worker-a", lease_until: "2026-07-17T00:01:00Z", lease_expires: "2026-07-17T00:01:00Z" }],
  scan_leases: [{ library_id: 12, scan_task_id: 71, owner_id: "scanner-a", lease_until: "2026-07-17T00:02:00Z", expired: true }],
  resource_budget: { global_limit: 8, global_used: 5, poster_limit: 3, poster_used: 2, preview_limit: 4, preview_used: 3 },
  sqlite_metrics: { scope: "process_since_start", persistent: false, busy_retries: 11, busy_exhausted: 2, progress_batches: 13, log_batches: 17, log_failures: 5, dropped_logs: 7 },
  publication_policy: [],
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

describe("AdminConsolePage", () => {
  beforeEach(() => {
    fetchAdminOverviewMock.mockReset();
    authState.token = "admin-token";
    MockEventSource.instances = [];
    vi.stubGlobal("EventSource", MockEventSource);
  });

  it("uses exact row keys and renders scan task IDs with a null fallback", async () => {
    const data = overview(54, "row-key-overview");
    data.running_post_ingest_tasks.push({ ...data.running_post_ingest_tasks[0], id: 902, scan_task_id: null, lease_owner: "poster-worker-null" });
    fetchAdminOverviewMock.mockResolvedValueOnce(data);
    render(<AdminConsolePage />);

    const running = await screen.findByTestId("running-table");
    const leases = screen.getByTestId("lease-table");
    expect(running).toHaveAttribute("data-row-key", "id");
    expect(leases).toHaveAttribute("data-row-key", "library_id");
    expect(running).toHaveTextContent("71");
    expect(running).toHaveTextContent("-");
  });

  it("defensively displays only the first 50 running tasks", async () => {
    const data = overview(53, "fifty-overview");
    data.running_post_ingest_tasks = Array.from({ length: 51 }, (_, index) => ({
      ...data.running_post_ingest_tasks[0], id: 1001 + index, lease_owner: `worker-${index + 1}`,
    }));
    fetchAdminOverviewMock.mockResolvedValueOnce(data);
    render(<AdminConsolePage />);

    const running = await screen.findByTestId("running-table");
    expect(running).toHaveTextContent("worker-1");
    expect(running).toHaveTextContent("worker-50");
    expect(running).not.toHaveTextContent("worker-51");
  });

  it("rejects SSE activities and resource array items with invalid field types", async () => {
    const rest = deferred<AdminOverview>();
    let signal: AbortSignal | undefined;
    fetchAdminOverviewMock.mockImplementation((value?: AbortSignal) => { signal = value; return rest.promise; });
    render(<AdminConsolePage />);
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));
    const stream = MockEventSource.instances[0];
    const invalidPayloads = [
      { ...overview(60, "bad-activity"), activities: [{ ...overview(60, "x").activities[0], media_id: "1" }] },
      { ...overview(61, "bad-running"), running_post_ingest_tasks: [{ ...overview(61, "x").running_post_ingest_tasks[0], lease_owner: 42 }] },
      { ...overview(62, "bad-lease"), scan_leases: [{ ...overview(62, "x").scan_leases[0], expired: "yes" }] },
    ];
    act(() => invalidPayloads.forEach((payload) => stream.emitRaw(JSON.stringify(payload))));

    expect(signal?.aborted).toBe(false);
    for (const message of ["bad-activity", "bad-running", "bad-lease"]) expect(screen.queryByText(message)).not.toBeInTheDocument();
    await act(async () => rest.resolve(overview(63, "valid-after-invalid-items")));
    expect(screen.getByText("valid-after-invalid-items")).toBeInTheDocument();
  });

  it("has natural Task22 translations without replacement placeholders", () => {
    expect(en.pages.admin_console.sqlite_process_scope).toBe("Process-local metrics since startup (not persisted)");
    expect(zhCN.pages.admin_console.resource_control).toBe("\u8d44\u6e90\u63a7\u5236");
    expect(zhTW.pages.admin_console.resource_control).toBe("\u8cc7\u6e90\u63a7\u5236");
    for (const locale of [en, zhCN, zhTW]) {
      for (const key of ["resource_control", "queue_by_status", "queue_by_type", "oldest_waiting_seconds", "seconds", "expired_lease_count", "running_post_ingest_tasks", "scan_leases", "col_task_id", "col_task_type", "col_attempt", "col_run_seconds", "col_started_at", "col_lease_owner", "col_lease_until", "col_library_id", "col_scan_task_id", "col_owner_id", "col_status", "expired", "active", "budget_global", "budget_poster", "budget_preview", "sqlite_metrics", "sqlite_process_scope", "busy_retries", "busy_exhausted", "progress_batches", "log_batches", "log_failures", "dropped_logs"] as const) {
        expect(locale.pages.admin_console[key]).not.toMatch(/[?\uFFFD]/);
      }
    }
  });

  it("renders resource-control queue, running tasks, leases, budgets, and process-scoped SQLite metrics", async () => {
    fetchAdminOverviewMock.mockResolvedValueOnce(overview(55, "resource-overview"));
    render(<AdminConsolePage />);

    expect(await screen.findByText("resource-overview")).toBeInTheDocument();
    for (const value of ["failed: 2", "running: 1", "waiting: 3", "poster / failed: 2", "preview / waiting: 3", "127pages.admin_console.seconds", "901", "poster-worker-a", "scanner-a", "5 / 8", "2 / 3", "3 / 4", "11", "13", "17", "7"]) {
      expect(screen.getByText(value)).toBeInTheDocument();
    }
    expect(screen.queryByText("preview / waiting: 99")).not.toBeInTheDocument();
    for (const key of ["expired_lease_count", "busy_retries", "busy_exhausted", "progress_batches", "log_batches", "log_failures", "dropped_logs", "sqlite_process_scope", "expired"]) {
      expect(screen.getByText(`pages.admin_console.${key}`)).toBeInTheDocument();
    }
  });

  it("rejects SSE overviews with malformed resource-control payloads", async () => {
    const rest = deferred<AdminOverview>();
    let signal: AbortSignal | undefined;
    fetchAdminOverviewMock.mockImplementation((value?: AbortSignal) => { signal = value; return rest.promise; });
    render(<AdminConsolePage />);
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));

    const malformed = { ...overview(66, "malformed-resource"), resource_budget: { global_limit: "eight" } };
    act(() => MockEventSource.instances[0].emitRaw(JSON.stringify(malformed)));

    expect(signal?.aborted).toBe(false);
    expect(screen.queryByText("malformed-resource")).not.toBeInTheDocument();
    await act(async () => rest.resolve(overview(67, "valid-resource")));
    expect(screen.getByText("valid-resource")).toBeInTheDocument();
  });

  it("uses the first SSE overview, stops loading, aborts REST, and ignores its stale result", async () => {
    const rest = deferred<AdminOverview>();
    let signal: AbortSignal | undefined;
    fetchAdminOverviewMock.mockImplementation((value?: AbortSignal) => { signal = value; return rest.promise; });
    render(<AdminConsolePage />);

    await waitFor(() => expect(fetchAdminOverviewMock).toHaveBeenCalledTimes(1));
    expect(screen.getAllByRole("status").length).toBeGreaterThan(0);

    act(() => MockEventSource.instances[0].emitOverview(overview(61, "from-sse")));
    expect(screen.queryAllByRole("status")).toHaveLength(0);
    expect(screen.getAllByText("61%")[0]).toBeInTheDocument();
    expect(screen.getByText("from-sse")).toBeInTheDocument();
    expect(signal?.aborted).toBe(true);

    await act(async () => rest.resolve(overview(12, "stale-rest")));
    expect(screen.getAllByText("61%")[0]).toBeInTheDocument();
    expect(screen.queryByText("stale-rest")).not.toBeInTheDocument();
  });

  it("ignores an old effect generation without aborting the new REST attempt", async () => {
    const firstRest = deferred<AdminOverview>();
    const secondRest = deferred<AdminOverview>();
    const signals: AbortSignal[] = [];
    fetchAdminOverviewMock
      .mockImplementationOnce((signal?: AbortSignal) => {
        if (signal) signals.push(signal);
        return firstRest.promise;
      })
      .mockImplementationOnce((signal?: AbortSignal) => {
        if (signal) signals.push(signal);
        return secondRest.promise;
      });
    const view = render(<AdminConsolePage />);
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));
    const oldStream = MockEventSource.instances[0];

    authState.token = "replacement-token";
    view.rerender(<AdminConsolePage />);
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(2));
    const newStream = MockEventSource.instances[1];
    expect(signals).toHaveLength(2);
    expect(signals[0].aborted).toBe(true);
    expect(signals[1].aborted).toBe(false);

    act(() => oldStream.emitOverview(overview(31, "old-generation")));
    expect(signals[1].aborted).toBe(false);
    expect(screen.queryByText("old-generation")).not.toBeInTheDocument();

    act(() => newStream.emitOverview(overview(91, "new-generation")));
    expect(signals[1].aborted).toBe(true);
    expect(screen.getByText("new-generation")).toBeInTheDocument();
    expect(screen.getAllByText("91%")[0]).toBeInTheDocument();
  });

  it("ignores malformed and structurally invalid SSE until REST supplies valid data", async () => {
    const rest = deferred<AdminOverview>();
    let signal: AbortSignal | undefined;
    fetchAdminOverviewMock.mockImplementation((value?: AbortSignal) => {
      signal = value;
      return rest.promise;
    });
    render(<AdminConsolePage />);
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));
    const stream = MockEventSource.instances[0];

    act(() => {
      stream.emitRaw("not-json");
      stream.emitRaw("{}");
      stream.emitRaw("[]");
    });

    expect(signal?.aborted).toBe(false);
    expect(screen.getAllByRole("status").length).toBeGreaterThan(0);

    await act(async () => rest.resolve(overview(47, "rest-after-invalid-sse")));
    expect(screen.queryAllByRole("status")).toHaveLength(0);
    expect(screen.getByText("rest-after-invalid-sse")).toBeInTheDocument();
    expect(screen.getAllByText("47%")[0]).toBeInTheDocument();
  });

  it("closes the stream, aborts REST, and clears the refresh timer on unmount", async () => {
    const rest = deferred<AdminOverview>();
    let signal: AbortSignal | undefined;
    fetchAdminOverviewMock.mockImplementation((value?: AbortSignal) => { signal = value; return rest.promise; });
    const clearIntervalSpy = vi.spyOn(window, "clearInterval");
    const view = render(<AdminConsolePage />);
    await waitFor(() => expect(fetchAdminOverviewMock).toHaveBeenCalled());
    const stream = MockEventSource.instances[0];

    view.unmount();

    expect(stream.close).toHaveBeenCalledTimes(1);
    expect(signal?.aborted).toBe(true);
    expect(clearIntervalSpy).toHaveBeenCalled();
    clearIntervalSpy.mockRestore();
  });

  it("ends loading and offers retry when REST fails before any SSE overview", async () => {
    fetchAdminOverviewMock.mockRejectedValueOnce(new Error("timeout"));
    render(<AdminConsolePage />);

    expect(await screen.findByRole("alert")).toHaveTextContent("common.loading_failed");
    expect(screen.queryAllByRole("status")).toHaveLength(0);
    expect(screen.getByRole("button", { name: "common.retry" })).toBeInTheDocument();
    expect(fetchAdminOverviewMock.mock.calls[0][0]).toBeInstanceOf(AbortSignal);
  });

  it("retries REST with a fresh controller and renders the successful overview", async () => {
    const retryResult = deferred<AdminOverview>();
    const signals: AbortSignal[] = [];
    fetchAdminOverviewMock
      .mockImplementationOnce((signal?: AbortSignal) => {
        if (signal) signals.push(signal);
        return Promise.reject(new Error("timeout"));
      })
      .mockImplementationOnce((signal?: AbortSignal) => {
        if (signal) signals.push(signal);
        return retryResult.promise;
      });
    render(<AdminConsolePage />);

    const retry = await screen.findByRole("button", { name: "common.retry" });
    expect(screen.queryAllByRole("status")).toHaveLength(0);
    expect(signals).toHaveLength(1);
    expect(signals[0].aborted).toBe(false);

    fireEvent.click(retry);

    expect(fetchAdminOverviewMock).toHaveBeenCalledTimes(2);
    expect(signals).toHaveLength(2);
    expect(signals[0].aborted).toBe(true);
    expect(signals[1]).not.toBe(signals[0]);
    expect(signals[1].aborted).toBe(false);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getAllByRole("status").length).toBeGreaterThan(0);
    expect(MockEventSource.instances).toHaveLength(1);

    await act(async () => retryResult.resolve(overview(83, "retry-success")));

    expect(screen.queryAllByRole("status")).toHaveLength(0);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getAllByText("83%")[0]).toBeInTheDocument();
    expect(screen.getByText("retry-success")).toBeInTheDocument();
  });

  it("keeps existing overview when the SSE connection errors", async () => {
    fetchAdminOverviewMock.mockReturnValue(new Promise(() => undefined));
    render(<AdminConsolePage />);
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));
    const stream = MockEventSource.instances[0];

    act(() => stream.emitOverview(overview(72, "keep-me")));
    expect(screen.getByText("pages.admin_console.stream_connected")).toBeInTheDocument();
    act(() => stream.emitError());

    expect(screen.getByText("keep-me")).toBeInTheDocument();
    expect(screen.getAllByText("72%")[0]).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.polling_mode")).toBeInTheDocument();
  });

  it("does not render publication policy diagnostics on the console", async () => {
    const data = overview(70, "publication-policy-overview");
    data.publication_policy = [{
      media_id: 10,
      run_id: 100,
      generation: 2,
      policy_version: 2,
      status: "failed",
      terminal_reason: "required_failed",
      required_waiting: 0,
      required_failed: 1,
      optional_waiting: 1,
      optional_failed: 0,
      adapter_unavailable: ["scrape", "prepare"],
      metadata_errors: ["ffprobe: duration unavailable"],
      recovery_error: "asset recovery failed",
    }];
    fetchAdminOverviewMock.mockResolvedValueOnce(data);
    render(<AdminConsolePage />);

    expect(await screen.findByText("publication-policy-overview")).toBeInTheDocument();
    expect(screen.queryByText("pages.admin_console.publication_policy")).not.toBeInTheDocument();
    expect(screen.queryByTestId("publication-policy-table")).not.toBeInTheDocument();
  });

  it("rejects SSE overviews with malformed publication_policy payloads while loading continues", async () => {
    const rest = deferred<AdminOverview>();
    let signal: AbortSignal | undefined;
    fetchAdminOverviewMock.mockImplementation((value?: AbortSignal) => { signal = value; return rest.promise; });
    render(<AdminConsolePage />);
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));
    expect(screen.getAllByRole("status").length).toBeGreaterThan(0);

    const malformed = { ...overview(71, "malformed-publication"), publication_policy: [{ media_id: "bad" }] };
    act(() => MockEventSource.instances[0].emitRaw(JSON.stringify(malformed)));

    expect(signal?.aborted).toBe(false);
    expect(screen.queryByText("malformed-publication")).not.toBeInTheDocument();
    expect(screen.getAllByRole("status").length).toBeGreaterThan(0);
    await act(async () => rest.resolve(overview(72, "valid-after-malformed-publication")));
    expect(screen.getByText("valid-after-malformed-publication")).toBeInTheDocument();
  });
});
