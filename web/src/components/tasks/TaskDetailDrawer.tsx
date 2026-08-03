import { useCallback, useEffect, useState } from "react";
import type { DetailResult } from "../../api/taskControl";
import { fetchTaskControlDetail } from "../../api/taskControl";

export interface TaskDetailDrawerProps {
  taskId: string | null;
  onClose: () => void;
}

export function TaskDetailDrawer({ taskId, onClose }: TaskDetailDrawerProps) {
  const [detail, setDetail] = useState<DetailResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!taskId) return;
    setLoading(true);
    setError(null);
    try {
      const result = await fetchTaskControlDetail(taskId);
      if (result) {
        setDetail(result);
      } else {
        setError("Task not found");
      }
    } catch {
      setError("Failed to load detail");
    } finally {
      setLoading(false);
    }
  }, [taskId]);

  useEffect(() => {
    if (taskId) {
      void load();
    } else {
      setDetail(null);
    }
  }, [taskId, load]);

  if (!taskId) return null;

  return (
    <div
      role="dialog"
      aria-label={`Task detail: ${taskId}`}
      style={{
        position: "fixed",
        right: 0,
        top: 0,
        width: 480,
        height: "100vh",
        background: "#0a0a0a",
        borderLeft: "1px solid #303030",
        padding: "24px 20px",
        overflowY: "auto",
        zIndex: 1000,
        boxShadow: "-4px 0 24px rgba(0,0,0,0.5)",
      }}
    >
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 20 }}>
        <h2 style={{ fontSize: 16, fontWeight: 600, color: "#d9d9d9", margin: 0 }}>Task Detail</h2>
        <button
          onClick={onClose}
          aria-label="Close"
          style={{
            background: "none",
            border: "none",
            color: "#888",
            fontSize: 20,
            cursor: "pointer",
            padding: "4px 8px",
          }}
        >
          ×
        </button>
      </div>

      {loading && <div style={{ color: "#888", textAlign: "center", padding: 20 }}>Loading...</div>}

      {error && (
        <div style={{ textAlign: "center", padding: 20 }}>
          <p style={{ color: "#ff4d4f" }}>{error}</p>
          <button onClick={load} style={{ padding: "6px 16px", background: "#1677ff", color: "#fff", border: "none", borderRadius: 4, cursor: "pointer" }}>Retry</button>
        </div>
      )}

      {detail && !loading && (
        <div>
          <div style={{ marginBottom: 16 }}>
            <div style={{ fontSize: 14, fontFamily: "monospace", color: "#1677ff" }}>{detail.row.task_id}</div>
            <div style={{ fontSize: 13, color: "#888", marginTop: 4 }}>
              Type: {detail.row.task_type} · Family: {detail.row.family}
            </div>
            <div style={{ fontSize: 12, color: "#666", marginTop: 2 }}>
              Status: {detail.row.normalized_status} (raw: {detail.row.raw_status}) · Revision: {detail.row.revision}
            </div>
            <div style={{ fontSize: 12, color: "#666", marginTop: 2 }}>
              Gen {detail.row.generation} · Retry Round {detail.row.retry_round} · Attempt {detail.row.attempt}/{detail.row.max_attempts}
            </div>
          </div>

          {detail.row.terminal_reason && (
            <div style={{ background: "#1a0a0a", border: "1px solid #ff4d4f33", borderRadius: 4, padding: 8, marginBottom: 16 }}>
              <div style={{ fontSize: 11, color: "#ff4d4f", textTransform: "uppercase", marginBottom: 4 }}>Terminal Reason</div>
              <div style={{ fontSize: 13, color: "#ff9999" }}>{detail.row.terminal_reason}</div>
            </div>
          )}

          {detail.row.removed_at && (
            <div style={{ background: "#1a1a0a", border: "1px solid #faad1433", borderRadius: 4, padding: 8, marginBottom: 16 }}>
              <div style={{ fontSize: 11, color: "#faad14", textTransform: "uppercase", marginBottom: 4 }}>Removed</div>
              <div style={{ fontSize: 12, color: "#aaa" }}>At: {new Date(detail.row.removed_at).toLocaleString()}</div>
              {detail.row.removed_by && <div style={{ fontSize: 12, color: "#aaa" }}>By: {detail.row.removed_by}</div>}
              {detail.row.remove_reason && <div style={{ fontSize: 12, color: "#aaa" }}>Reason: {detail.row.remove_reason}</div>}
            </div>
          )}

          {detail.row.owner_lease && (
            <div style={{ marginBottom: 16, padding: 8, background: "#0a0a1a", borderRadius: 4, border: "1px solid #1677ff33" }}>
              <div style={{ fontSize: 11, color: "#1677ff", textTransform: "uppercase", marginBottom: 4 }}>Owner Lease</div>
              <div style={{ fontSize: 12, color: "#aaa" }}>Owner: {detail.row.owner_lease.owner}</div>
              {detail.row.owner_lease.lease_until && <div style={{ fontSize: 12, color: "#aaa" }}>Lease Until: {new Date(detail.row.owner_lease.lease_until).toLocaleString()}</div>}
            </div>
          )}

          {detail.attempts && detail.attempts.length > 0 && (
            <section aria-label="Attempts" style={{ marginBottom: 16 }}>
              <h3 style={{ fontSize: 13, color: "#d9d9d9", marginBottom: 8 }}>Attempts</h3>
              {detail.attempts.map((a, i) => (
                <div key={i} style={{ padding: "6px 8px", borderBottom: "1px solid #1a1a1a", fontSize: 12, color: "#888" }}>
                  #{a.attempt}: {a.status} {a.error ? `· ${a.error}` : ""} {a.duration_secs ? `(${a.duration_secs}s)` : ""}
                </div>
              ))}
            </section>
          )}

          {detail.audit_events && detail.audit_events.length > 0 && (
            <section aria-label="Audit events" style={{ marginBottom: 16 }}>
              <h3 style={{ fontSize: 13, color: "#d9d9d9", marginBottom: 8 }}>Audit</h3>
              {detail.audit_events.map((e) => (
                <div key={e.id} style={{ padding: "4px 8px", borderBottom: "1px solid #1a1a1a", fontSize: 11, color: "#666" }}>
                  [{e.created_at}] {e.action} by {e.actor_name}
                  {e.reason ? `: ${e.reason}` : ""}
                </div>
              ))}
            </section>
          )}
        </div>
      )}
    </div>
  );
}
