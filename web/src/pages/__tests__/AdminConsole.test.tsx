import React from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminOverview } from "../../api/client";

const { fetchAdminOverviewMock, authState } = vi.hoisted(() => ({
  fetchAdminOverviewMock: vi.fn(),
  authState: { token: "admin-token" },
}));

vi.mock("../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/client")>();
  return { ...actual, fetchAdminOverview: fetchAdminOverviewMock };
});

vi.mock("../../i18n", () => ({
  useT: () => (key: string, params?: Record<string, unknown>) => key + (params?.count ? `-${params.count}` : ""),
}));
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
      return <div data-testid="activities-table" data-row-key={typeof rowKey === "string" ? rowKey : "function"}>{dataSource.map((row, index) => <div key={index}>{columns.map((column, columnIndex) => <span key={columnIndex}>{column.render ? column.render(column.dataIndex ? row[column.dataIndex] : undefined, row) : String(column.dataIndex ? row[column.dataIndex] ?? "" : "")}</span>)}</div>)}</div>;
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
    this.listeners.get("overview")?.(new MessageEvent("overview", { data: JSON.stringify(data) }));
  }

  emitError() {
    this.onerror?.(new Event("error"));
  }
}

const systemOverview = (cpu: number, message: string): AdminOverview => ({
  monitor: { cpu_percent: cpu, memory_percent: 20, disk_percent: 30 },
  system: { cpu_count: 8, memory_total: 16 * 1024 ** 3, os: `os-${cpu}`, database: "sqlite", software_version: "test" },
  activities: [{ id: cpu, username: "admin", action: "scan", media_id: 1, message, created_at: "2026-07-17T00:00:00Z" }],
  sqlite_metrics: { scope: "process_since_start", persistent: false, busy_retries: 11, busy_exhausted: 2, progress_batches: 13, log_batches: 17, log_failures: 5, dropped_logs: 7 },
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

describe("AdminConsolePage (system-only)", () => {
  beforeEach(() => {
    fetchAdminOverviewMock.mockReset();
    authState.token = "admin-token";
    MockEventSource.instances = [];
    vi.stubGlobal("EventSource", MockEventSource);
  });

  it("renders system monitor (CPU, memory, disk) and system info", async () => {
    fetchAdminOverviewMock.mockResolvedValueOnce(systemOverview(54, "system-test"));
    render(<AdminConsolePage />);

    expect(await screen.findByText("system-test")).toBeInTheDocument();
    // System monitor titles
    expect(screen.getByText("pages.admin_console.cpu_usage")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.memory_usage")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.disk_usage")).toBeInTheDocument();
    // CPU values
    expect(screen.getAllByText("54%")[0]).toBeInTheDocument();
    expect(screen.getAllByText("20%")[0]).toBeInTheDocument();
    expect(screen.getAllByText("30%")[0]).toBeInTheDocument();
  });

  it("renders SQLite metrics (system scope only)", async () => {
    fetchAdminOverviewMock.mockResolvedValueOnce(systemOverview(55, "sqlite-test"));
    render(<AdminConsolePage />);

    expect(await screen.findByText("pages.admin_console.busy_retries")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.busy_exhausted")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.progress_batches")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.log_batches")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.log_failures")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.dropped_logs")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.sqlite_process_scope")).toBeInTheDocument();
  });

  it("renders system info section (CPU count, memory, OS, database, version)", async () => {
    fetchAdminOverviewMock.mockResolvedValueOnce(systemOverview(56, "system-info-test"));
    render(<AdminConsolePage />);

    expect(await screen.findByText("system-info-test")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.cpu_count")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.memory_size")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.os")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.database")).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.software_version")).toBeInTheDocument();
  });

  it("renders current activities table", async () => {
    fetchAdminOverviewMock.mockResolvedValueOnce(systemOverview(57, "activity-test"));
    render(<AdminConsolePage />);

    expect(await screen.findByText("activity-test")).toBeInTheDocument();
    expect(screen.getByTestId("activities-table")).toBeInTheDocument();
  });

  it("does NOT render task control fields on the console", async () => {
    fetchAdminOverviewMock.mockResolvedValueOnce(systemOverview(58, "no-tasks"));
    render(<AdminConsolePage />);

    expect(await screen.findByText("no-tasks")).toBeInTheDocument();
    // These task-related keys must not appear anywhere on the Console
    const taskKeys = [
      "post_ingest_queue",
      "task_alignment",
      "running_post_ingest_tasks",
      "scan_leases",
      "resource_budget",
      "resource_control",
      "queue_by_status",
      "queue_by_type",
      "oldest_waiting_seconds",
      "expired_lease_count",
      "budget_global",
      "budget_poster",
      "col_task_id",
      "col_task_type",
      "col_attempt",
      "col_lease_owner",
    ];
    for (const key of taskKeys) {
      expect(screen.queryByText(new RegExp(key, "i"))).not.toBeInTheDocument();
    }
  });

  it("uses the first SSE overview, stops loading, and aborts REST", async () => {
    const rest = deferred<AdminOverview>();
    let signal: AbortSignal | undefined;
    fetchAdminOverviewMock.mockImplementation((value?: AbortSignal) => { signal = value; return rest.promise; });
    render(<AdminConsolePage />);

    await waitFor(() => expect(fetchAdminOverviewMock).toHaveBeenCalledTimes(1));
    expect(screen.getAllByRole("status").length).toBeGreaterThan(0);

    act(() => MockEventSource.instances[0].emitOverview(systemOverview(61, "from-sse")));
    expect(screen.queryAllByRole("status")).toHaveLength(0);
    expect(screen.getAllByText("61%")[0]).toBeInTheDocument();
    expect(screen.getByText("from-sse")).toBeInTheDocument();
    expect(signal?.aborted).toBe(true);

    await act(async () => rest.resolve(systemOverview(12, "stale-rest")));
    expect(screen.getAllByText("61%")[0]).toBeInTheDocument();
    expect(screen.queryByText("stale-rest")).not.toBeInTheDocument();
  });

  it("ignores old generation without aborting new REST", async () => {
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

    act(() => oldStream.emitOverview(systemOverview(31, "old-generation")));
    expect(signals[1].aborted).toBe(false);
    expect(screen.queryByText("old-generation")).not.toBeInTheDocument();

    act(() => newStream.emitOverview(systemOverview(91, "new-generation")));
    expect(signals[1].aborted).toBe(true);
    expect(screen.getByText("new-generation")).toBeInTheDocument();
    expect(screen.getAllByText("91%")[0]).toBeInTheDocument();
  });

  it("ignores malformed SSE until REST supplies valid data", async () => {
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
      (stream as unknown as { emitRaw: (data: string) => void }).emitRaw?.("not-json");
    });

    expect(signal?.aborted).toBe(false);
    expect(screen.getAllByRole("status").length).toBeGreaterThan(0);

    await act(async () => rest.resolve(systemOverview(47, "rest-after-invalid-sse")));
    expect(screen.queryAllByRole("status")).toHaveLength(0);
    expect(screen.getByText("rest-after-invalid-sse")).toBeInTheDocument();
    expect(screen.getAllByText("47%")[0]).toBeInTheDocument();
  });

  it("closes stream, aborts REST, and clears timer on unmount", async () => {
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

  it("offers retry when REST fails before any SSE overview", async () => {
    fetchAdminOverviewMock.mockRejectedValueOnce(new Error("timeout"));
    render(<AdminConsolePage />);

    expect(await screen.findByRole("alert")).toHaveTextContent("common.loading_failed");
    expect(screen.queryAllByRole("status")).toHaveLength(0);
    expect(screen.getByRole("button", { name: "common.retry" })).toBeInTheDocument();
    expect(fetchAdminOverviewMock.mock.calls[0][0]).toBeInstanceOf(AbortSignal);
  });

  it("keeps existing overview when SSE connection errors", async () => {
    fetchAdminOverviewMock.mockReturnValue(new Promise(() => undefined));
    render(<AdminConsolePage />);
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));
    const stream = MockEventSource.instances[0];

    act(() => stream.emitOverview(systemOverview(72, "keep-me")));
    expect(screen.getByText("pages.admin_console.stream_connected")).toBeInTheDocument();
    act(() => stream.emitError());

    expect(screen.getByText("keep-me")).toBeInTheDocument();
    expect(screen.getAllByText("72%")[0]).toBeInTheDocument();
    expect(screen.getByText("pages.admin_console.polling_mode")).toBeInTheDocument();
  });

  it("does not render publication policy diagnostics on the console (no pub table)", async () => {
    const data = systemOverview(70, "console-no-pub");
    fetchAdminOverviewMock.mockResolvedValueOnce(data);
    render(<AdminConsolePage />);

    expect(await screen.findByText("console-no-pub")).toBeInTheDocument();
    expect(screen.queryByText("pages.admin_console.publication_policy")).not.toBeInTheDocument();
    expect(screen.queryByTestId("publication-policy-table")).not.toBeInTheDocument();
  });

  it("rejects task-control fields in SSE payloads (guard catches non-system-only data)", async () => {
    const rest = deferred<AdminOverview>();
    let signal: AbortSignal | undefined;
    fetchAdminOverviewMock.mockImplementation((value?: AbortSignal) => { signal = value; return rest.promise; });
    render(<AdminConsolePage />);
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));

    // SSE payload with task-control fields must be rejected by the guard
    const taskControlPayload = JSON.stringify({
      monitor: { cpu_percent: 99, memory_percent: 50, disk_percent: 40 },
      system: { cpu_count: 4, memory_total: 8000000000, os: "linux", database: "sqlite", software_version: "v1" },
      activities: [{ id: 1, username: "u", action: "a", media_id: 0, message: "m", created_at: "2026-01-01T00:00:00Z" }],
      post_ingest_queue: { by_status: { waiting: 1 }, by_type: {}, oldest_waiting_seconds: 0, expired_lease_count: 0 },
      task_alignment: { by_type: {} },
      running_post_ingest_tasks: [],
      scan_leases: [],
      resource_budget: { global_limit: 1, global_used: 0, poster_limit: 1, poster_used: 0, preview_limit: 1, preview_used: 0 },
      sqlite_metrics: { scope: "process_since_start", persistent: false, busy_retries: 0, busy_exhausted: 0, progress_batches: 0, log_batches: 0, log_failures: 0, dropped_logs: 0 },
      publication_policy: [],
    });

    // Dispatch raw SSE with task control fields
    act(() => {
      const stream = MockEventSource.instances[0];
      stream.listeners.get("overview")?.(new MessageEvent("overview", { data: taskControlPayload }));
    });

    expect(signal?.aborted).toBe(false);
    expect(screen.queryByText("99%")).not.toBeInTheDocument();
    await act(async () => rest.resolve(systemOverview(71, "after-rejected-task-sse")));
    expect(screen.getByText("after-rejected-task-sse")).toBeInTheDocument();
  });
});
