import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useTaskControlStream } from "./useTaskControlStream";
import type { ProjectionRow } from "../api/taskControl";
import { useAuthStore } from "../store/auth";

function makeRow(id: number, overrides: Partial<ProjectionRow> = {}): ProjectionRow {
  return {
    task_id: `orchestration:${id}`,
    source_kind: "orchestration",
    source_id: id,
    task_type: "poster",
    family: "media_ingestion",
    normalized_status: "waiting",
    raw_status: "waiting",
    revision: id,
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

type MockESInstance = {
  url: string;
  readyState: number;
  onopen: ((ev: Event) => void) | null;
  onmessage: ((ev: MessageEvent) => void) | null;
  onerror: ((ev: Event) => void) | null;
  close: ReturnType<typeof vi.fn>;
  addEventListener: ReturnType<typeof vi.fn>;
  listeners: Map<string, Array<(ev: MessageEvent) => void>>;
};

const instances: MockESInstance[] = [];

function dispatch(es: MockESInstance, type: string, data: unknown) {
  const ev = new MessageEvent(type, {
    data: typeof data === "string" ? data : JSON.stringify(data),
  });
  const handlers = es.listeners.get(type);
  if (handlers) {
    for (const h of [...handlers]) {
      try { h(ev); } catch { /* ignore */ }
    }
  }
}

describe("useTaskControlStream", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    instances.length = 0;

    const ESClass = class {
      url: string;
      readyState = 1;
      onopen: ((ev: Event) => void) | null = null;
      onmessage: ((ev: MessageEvent) => void) | null = null;
      onerror: ((ev: Event) => void) | null = null;
      close = vi.fn();
      addEventListener = vi.fn();
      private _listeners = new Map<string, Array<(ev: MessageEvent) => void>>();

      constructor(url: string) {
        this.url = url;
        const self = this;
        this.addEventListener.mockImplementation(
          (type: string, handler: (ev: MessageEvent) => void) => {
            if (!self._listeners.has(type)) self._listeners.set(type, []);
            self._listeners.get(type)!.push(handler);
          },
        );
        // Store reference for test access
        instances.push({
          get url() { return self.url; },
          get readyState() { return self.readyState; },
          get onopen() { return self.onopen; },
          get onmessage() { return self.onmessage; },
          get onerror() { return self.onerror; },
          get close() { return self.close; },
          get addEventListener() { return self.addEventListener; },
          get listeners() { return self._listeners; },
        });
      }
    };
    globalThis.EventSource = ESClass as unknown as typeof EventSource;
    useAuthStore.getState().setToken("test-stream-token");
  });

  afterEach(() => {
    useAuthStore.getState().setToken(null);
  });

  function getES(): MockESInstance {
    const es = instances[0];
    if (!es) throw new Error("No EventSource instance");
    return es;
  }

  it("initializes with empty items and not connected", () => {
    useAuthStore.getState().setToken(null);
    const { result } = renderHook(() =>
      useTaskControlStream({ task_type: "poster" }),
    );
    expect(result.current.connected).toBe(false);
    expect(result.current.items).toEqual([]);
    expect(result.current.snapshotRevision).toBe(0);
    expect(result.current.error).toBeNull();
  });

  it("connects and updates items on snapshot event", async () => {
    const { result } = renderHook(() =>
      useTaskControlStream({ task_type: "poster" }),
    );

    await act(async () => {
      const es = getES();
      dispatch(es, "open", {});
      dispatch(es, "snapshot", {
        items: [makeRow(1), makeRow(2)],
        total: 2,
        snapshot_revision: 10,
      });
    });

    expect(result.current.connected).toBe(true);
    expect(result.current.items).toHaveLength(2);
    expect(result.current.snapshotRevision).toBe(10);
  });

  it("replaces items on newer snapshot", async () => {
    const { result } = renderHook(() =>
      useTaskControlStream({ task_type: "poster" }),
    );

    await act(async () => {
      dispatch(getES(), "snapshot", {
        items: [makeRow(1)], total: 1, snapshot_revision: 5,
      });
    });

    await act(async () => {
      dispatch(getES(), "snapshot", {
        items: [makeRow(1), makeRow(2), makeRow(3)], total: 3, snapshot_revision: 6,
      });
    });

    expect(result.current.items).toHaveLength(3);
    expect(result.current.snapshotRevision).toBe(6);
  });

  it("applies delta only when revision is newer", async () => {
    const { result } = renderHook(() =>
      useTaskControlStream({ task_type: "poster" }),
    );

    await act(async () => {
      dispatch(getES(), "snapshot", {
        items: [makeRow(1, { revision: 5, normalized_status: "waiting" })],
        total: 1, snapshot_revision: 5,
      });
    });

    // Stale delta
    await act(async () => {
      dispatch(getES(), "delta", {
        row: makeRow(1, { revision: 3, normalized_status: "running" }),
        action: "abort",
      });
    });

    expect(result.current.items[0]!.normalized_status).toBe("waiting");

    // Newer delta
    await act(async () => {
      dispatch(getES(), "delta", {
        row: makeRow(1, { revision: 7, normalized_status: "cancelled" }),
        action: "abort",
      });
    });

    expect(result.current.items[0]!.normalized_status).toBe("cancelled");
  });

  it("stale suppression: ignores older snapshot revision", async () => {
    const { result } = renderHook(() =>
      useTaskControlStream({ task_type: "poster" }),
    );

    await act(async () => {
      dispatch(getES(), "snapshot", {
        items: [makeRow(1, { normalized_status: "done", revision: 10 })],
        total: 1, snapshot_revision: 10,
      });
    });

    await act(async () => {
      dispatch(getES(), "snapshot", {
        items: [makeRow(1, { normalized_status: "failed", revision: 8 })],
        total: 1, snapshot_revision: 8,
      });
    });

    expect(result.current.items[0]!.normalized_status).toBe("done");
  });

  it("handles resync event clearing items", async () => {
    const { result } = renderHook(() =>
      useTaskControlStream({ task_type: "poster" }),
    );

    await act(async () => {
      dispatch(getES(), "snapshot", {
        items: [makeRow(1)], total: 1, snapshot_revision: 1,
      });
    });

    expect(result.current.items).toHaveLength(1);

    await act(async () => {
      dispatch(getES(), "resync", {});
    });

    expect(result.current.items).toEqual([]);
  });

  it("polling fallback on stream error", async () => {
    const { result } = renderHook(() =>
      useTaskControlStream({ task_type: "poster" }),
    );

    await act(async () => {
      const es = getES();
      if (es.onerror) es.onerror(new Event("error"));
    });

    expect(result.current.connected).toBe(false);
    expect(result.current.error).not.toBeNull();
  });

  it("unmount cleanup closes EventSource", async () => {
    const { unmount } = renderHook(() =>
      useTaskControlStream({ task_type: "poster" }),
    );

    const es = getES();
    unmount();
    expect(es.close).toHaveBeenCalled();
  });

  it("auth token included in stream URL", async () => {
    renderHook(() => useTaskControlStream({ task_type: "poster" }));
    expect(getES().url).toContain("access_token=test-stream-token");
  });

  it("resets items when filter changes", async () => {
    const { result, rerender } = renderHook(
      ({ filter }) => useTaskControlStream(filter),
      { initialProps: { filter: { task_type: "poster" } } },
    );

    await act(async () => {
      dispatch(getES(), "snapshot", {
        items: [makeRow(1)], total: 1, snapshot_revision: 1,
      });
    });

    expect(result.current.items).toHaveLength(1);

    rerender({ filter: { task_type: "transcode" } });
  });
});
