import { useEffect, useRef, useState, useCallback } from "react";
import type { ProjectionRow } from "../api/taskControl";
import { fetchTaskControlList } from "../api/taskControl";
import type { TaskControlFilter } from "../lib/taskControlFilters";
import { useAuthStore } from "../store/auth";

export interface StreamState {
  items: ProjectionRow[];
  connected: boolean;
  snapshotRevision: number;
  error: string | null;
}

interface SnapshotEvent {
  items: ProjectionRow[];
  total: number;
  snapshot_revision: number;
  next_cursor?: string;
}

interface DeltaEvent {
  row: ProjectionRow;
  action: string;
}

/**
 * SSE stream reconciliation hook for real-time task control updates.
 *
 * Strategy:
 * 1. Opens SSE to /api/v1/admin/tasks/stream with auth token in URL.
 * 2. Snapshot events replace the entire item set.
 * 3. Delta events update individual rows only when the incoming revision
 *    is strictly greater than the currently tracked revision for that row.
 * 4. Resync events trigger a refetch via REST.
 * 5. On stream error or disconnect, falls back to periodic polling.
 * 6. Cleanly closes EventSource and stops timers on unmount.
 */
export function useTaskControlStream(
  filter: TaskControlFilter = {},
): StreamState {
  const token = useAuthStore((s) => s.token);
  const [items, setItems] = useState<ProjectionRow[]>([]);
  const [connected, setConnected] = useState(false);
  const [snapshotRevision, setSnapshotRevision] = useState(0);
  const [error, setError] = useState<string | null>(null);

  const activeRef = useRef(true);
  const esRef = useRef<EventSource | null>(null);
  const pollTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const pollAbortRef = useRef<AbortController | null>(null);
  const snapshotRevRef = useRef(0);

  const stopPolling = useCallback(() => {
    if (pollTimerRef.current !== null) {
      clearInterval(pollTimerRef.current);
      pollTimerRef.current = null;
    }
    pollAbortRef.current?.abort();
    pollAbortRef.current = null;
  }, []);

  const startPolling = useCallback(() => {
    stopPolling();
    pollTimerRef.current = setInterval(() => {
      const ctrl = new AbortController();
      pollAbortRef.current = ctrl;
      fetchTaskControlList({
        task_type: filter.task_type,
        status: filter.status,
        removed: filter.removed ?? "exclude",
      }).then((res) => {
        if (!activeRef.current) return;
        if (res.snapshot_revision > snapshotRevRef.current) {
          setItems(res.items);
          setSnapshotRevision(res.snapshot_revision);
          snapshotRevRef.current = res.snapshot_revision;
          setError(null);
        }
      }).catch(() => {
        if (!activeRef.current) return;
        setError("poll_failed");
      });
    }, 15000);
  }, [stopPolling, filter.task_type, filter.status, filter.removed]);

  // Main effect: manage SSE + polling lifecycle
  useEffect(() => {
    activeRef.current = true;
    setItems([]);
    setConnected(false);
    setError(null);
    setSnapshotRevision(0);
    snapshotRevRef.current = 0;

    const t = token;
    if (!t) {
      // No auth token — start polling immediately
      startPolling();
      return () => {
        activeRef.current = false;
        stopPolling();
      };
    }

    // Build SSE URL with auth token
    const url = `/api/v1/admin/tasks/stream?access_token=${encodeURIComponent(t)}`;
    const es = new EventSource(url);
    esRef.current = es;

    es.addEventListener("open", () => {
      if (!activeRef.current) return;
      setConnected(true);
      setError(null);
    });

    es.addEventListener("snapshot", (evt: MessageEvent) => {
      if (!activeRef.current) return;
      try {
        const data: SnapshotEvent = JSON.parse(evt.data);
        if (data.snapshot_revision <= snapshotRevRef.current) return;
        setItems(data.items);
        setSnapshotRevision(data.snapshot_revision);
        snapshotRevRef.current = data.snapshot_revision;
        setError(null);
        setConnected(true);
      } catch {
        // ignore malformed events
      }
    });

    es.addEventListener("delta", (evt: MessageEvent) => {
      if (!activeRef.current) return;
      try {
        const data: DeltaEvent = JSON.parse(evt.data);
        if (!data.row) return;
        setItems((prev) => {
          const idx = prev.findIndex((r) => r.task_id === data.row.task_id);
          if (idx === -1) {
            return [data.row, ...prev];
          }
          const current = prev[idx]!;
          if (data.row.revision <= current.revision) return prev;
          const next = [...prev];
          next[idx] = data.row;
          return next;
        });
      } catch {
        // ignore malformed events
      }
    });

    es.addEventListener("resync", () => {
      if (!activeRef.current) return;
      setItems([]);
      setConnected(false);
      stopPolling();
      startPolling();
    });

    es.addEventListener("heartbeat", () => {
      // no-op, keeps connection alive
    });

    es.onerror = () => {
      if (!activeRef.current) return;
      setConnected(false);
      setError("stream_disconnected");
      es.close();
      esRef.current = null;
      // Fall back to polling
      startPolling();
    };

    return () => {
      activeRef.current = false;
      es.close();
      esRef.current = null;
      stopPolling();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, startPolling, stopPolling]);

  return {
    items,
    connected,
    snapshotRevision,
    error,
  };
}
