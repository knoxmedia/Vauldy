import React, { useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import en from "../../i18n/locales/en.json";

const api = vi.hoisted(() => ({
  cancelTranscodeTask: vi.fn(),
  batchTranscodeTasks: vi.fn(),
}));

vi.mock("../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/client")>();
  return {
    ...actual,
    ...api,
    fetchTranscodeTasks: vi.fn().mockResolvedValue([
      { id: 1, file_id: "waiting.mp4", status: "waiting", progress: 0 },
      { id: 2, file_id: "failed.mp4", status: "failed", progress: 0 },
    ]),
    fetchScrapeTasks: vi.fn().mockResolvedValue([]),
    fetchScanTasks: vi.fn().mockResolvedValue([]),
    fetchSubtitleTasks: vi.fn().mockResolvedValue([]),
    fetchLyricTasks: vi.fn().mockResolvedValue([]),
    fetchPreviewTasks: vi.fn().mockResolvedValue([]),
    fetchAtrackTasks: vi.fn().mockResolvedValue([]),
    fetchKeyframeTasks: vi.fn().mockResolvedValue([]),
  };
});

vi.mock("../../i18n", () => ({
  useT: () => (key: string, values?: Record<string, unknown>) => {
    const value = key.split(".").reduce<unknown>((current, part) =>
      current && typeof current === "object" ? (current as Record<string, unknown>)[part] : undefined, en);
    let text = typeof value === "string" ? value : key;
    for (const [name, replacement] of Object.entries(values ?? {})) text = text.replace(`{${name}}`, String(replacement));
    return text;
  },
}));

vi.mock("@ant-design/icons", () => ({
  ClearOutlined: () => null, ClockCircleOutlined: () => null, DeleteOutlined: () => null,
  RedoOutlined: () => null, ReloadOutlined: () => null, StopOutlined: () => null, ThunderboltOutlined: () => null,
}));

vi.mock("antd", () => {
  const Wrapper = ({ children }: React.PropsWithChildren) => <div>{children}</div>;
  const Button = ({ icon, danger, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement> & { icon?: React.ReactNode; danger?: boolean }) => (
    <button {...props} data-danger={danger ? "true" : "false"}>{icon}</button>
  );
  const Popconfirm = ({ children, title, onConfirm }: React.PropsWithChildren<{ title: React.ReactNode; onConfirm: () => void }>) => {
    const [open, setOpen] = useState(false);
    return <span onClick={() => setOpen(true)}>{children}{open ? <button aria-label={`confirm ${String(title)}`} onClick={(event) => { event.stopPropagation(); onConfirm(); setOpen(false); }}>confirm</button> : null}</span>;
  };
  const Table = ({ dataSource = [], columns = [], rowKey, rowSelection }: {
    dataSource?: Array<Record<string, unknown>>;
    columns?: Array<{ title?: React.ReactNode; dataIndex?: string; render?: (value: unknown, row: Record<string, unknown>) => React.ReactNode }>;
    rowKey?: string;
    rowSelection?: { selectedRowKeys: React.Key[]; onChange: (keys: React.Key[]) => void };
  }) => <div role="table">
    <div>{columns.map((column, index) => <span key={index}>{column.title}</span>)}</div>
    {dataSource.map((row, rowIndex) => <div key={rowIndex}>
      {rowSelection ? <input aria-label={`select task ${String(row[rowKey ?? "id"])}`} type="checkbox" onChange={(event) => rowSelection.onChange(event.target.checked ? [row[rowKey ?? "id"] as React.Key] : [])} /> : null}
      {columns.map((column, columnIndex) => <span key={columnIndex}>{column.render ? column.render(column.dataIndex ? row[column.dataIndex] : undefined, row) : String(column.dataIndex ? row[column.dataIndex] ?? "" : "")}</span>)}
    </div>)}
  </div>;
  return {
    Button,
    Card: ({ children, title, extra }: React.PropsWithChildren<{ title?: React.ReactNode; extra?: React.ReactNode }>) => <section><h2>{title}</h2>{extra}{children}</section>,
    Popconfirm,
    Select: () => <select aria-label="status filter" />,
    Space: Wrapper,
    Table,
    Tabs: ({ items }: { items: Array<{ label: React.ReactNode; children: React.ReactNode }> }) => <div>{items.map((item, index) => <section key={index}><h1>{item.label}</h1>{item.children}</section>)}</div>,
    Tag: Wrapper,
    Tooltip: Wrapper,
    message: { error: vi.fn(), success: vi.fn(), warning: vi.fn() },
  };
});

import TaskManagerPage from "../TaskManager";

describe("TaskManager community surface", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.cancelTranscodeTask.mockResolvedValue(undefined);
    api.batchTranscodeTasks.mockResolvedValue({ ok: 1, failed: 0 });
  });

  it("omits commercial task surfaces and sensitive transcode columns", async () => {
    render(<TaskManagerPage />);
    await screen.findByText("waiting.mp4");
    for (const label of [/encrypt/i, /encryption/i, /pretranscode/i, /optimization/i, /^drm$/i, /^cleanup$/i]) {
      expect(screen.queryByText(label)).not.toBeInTheDocument();
    }
  });

  it("confirms a single cancel before calling its legacy API", async () => {
    render(<TaskManagerPage />);
    const cancel = await screen.findByRole("button", { name: "Cancel task" });
    expect(cancel).toHaveAttribute("data-danger", "false");
    fireEvent.click(cancel);
    expect(api.cancelTranscodeTask).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "confirm Confirm this action?" }));
    await waitFor(() => expect(api.cancelTranscodeTask).toHaveBeenCalledWith(1));
  });

  it("confirms a batch action and keeps command buttons accessible", async () => {
    render(<TaskManagerPage />);
    fireEvent.click(await screen.findByRole("checkbox", { name: "select task 1" }));
    const runNow = screen.getAllByRole("button", { name: "Run now" })[0];
    expect(runNow).toHaveAttribute("data-danger", "false");
    fireEvent.click(runNow);
    expect(api.batchTranscodeTasks).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "confirm Confirm this action?" }));
    await waitFor(() => expect(api.batchTranscodeTasks).toHaveBeenCalledWith("run_now", [1]));
    expect(screen.getAllByRole("button", { name: "Retry" }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("button", { name: "Stop" }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("button", { name: "Delete" }).every((button) => button.getAttribute("data-danger") === "false")).toBe(true);
  });
});
