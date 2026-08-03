import { useCallback, useEffect, useState } from "react";
import type { Overview, ProjectionRow } from "../../api/taskControl";
import { fetchTaskControlOverview } from "../../api/taskControl";

export interface TaskOverviewProps {
  onDrillDownType?: (taskType: string) => void;
  onSelectTask?: (taskId: string) => void;
}

function statusColor(status: string): string {
  switch (status) {
    case "waiting": return "#49aa19";
    case "running": return "#1677ff";
    case "done": return "#52c41a";
    case "failed": return "#ff4d4f";
    case "cancelled": return "#faad14";
    case "skipped": return "#888";
    default: return "#d9d9d9";
  }
}

export function TaskOverview({ onDrillDownType, onSelectTask }: TaskOverviewProps) {
  const [data, setData] = useState<Overview | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const ov = await fetchTaskControlOverview();
      setData(ov);
    } catch {
      setError("Failed to load overview");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading) {
    return <div role="status" aria-label="Loading overview" style={{ padding: 40, textAlign: "center", color: "#888" }}>Loading...</div>;
  }

  if (error) {
    return (
      <div style={{ padding: 40, textAlign: "center" }}>
        <p style={{ color: "#ff4d4f" }}>{error}</p>
        <button onClick={load} style={{ padding: "6px 16px", background: "#1677ff", color: "#fff", border: "none", borderRadius: 4, cursor: "pointer" }}>Retry</button>
      </div>
    );
  }

  if (!data) return null;

  return (
    <div>
      <section aria-label="Status counts">
        <h2 style={{ fontSize: 16, fontWeight: 600, color: "#d9d9d9", marginBottom: 12 }}>Status Summary</h2>
        <div style={{ display: "flex", gap: 12, flexWrap: "wrap", marginBottom: 20 }}>
          {Object.entries(data.status_counts).map(([status, count]) => (
            <div key={status} style={{ flex: "0 0 auto", minWidth: 100, padding: "12px 16px", background: "#111", borderRadius: 8, border: `1px solid ${statusColor(status)}`, textAlign: "center" }}>
              <div style={{ fontSize: 24, fontWeight: 700, color: statusColor(status) }}>{count}</div>
              <div style={{ fontSize: 12, color: "#888", textTransform: "uppercase" }}>{status}</div>
            </div>
          ))}
        </div>
      </section>

      <section aria-label="Type counts">
        <h2 style={{ fontSize: 16, fontWeight: 600, color: "#d9d9d9", marginBottom: 12 }}>Task Types</h2>
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap", marginBottom: 20 }}>
          {Object.entries(data.type_counts).sort(([,a], [,b]) => b - a).map(([type, count]) => (
            <button
              key={type}
              onClick={() => onDrillDownType?.(type)}
              style={{
                padding: "6px 14px",
                background: count > 0 ? "#1a1a1a" : "#0a0a0a",
                color: count > 0 ? "#d9d9d9" : "#555",
                border: "1px solid #303030",
                borderRadius: 4,
                cursor: "pointer",
                fontSize: 13,
                display: "flex",
                alignItems: "center",
                gap: 8,
              }}
              aria-label={`${type}: ${count} tasks`}
            >
              <span>{type}</span>
              <span style={{ fontWeight: 600, color: "#1677ff" }}>{count}</span>
            </button>
          ))}
        </div>
      </section>

      {renderSection("Running", data.running, "#1677ff", onSelectTask)}
      {renderSection("Oldest Waiting", data.oldest, "#49aa19", onSelectTask)}
      {renderSection("Blocked / Failed", data.blocked, "#ff4d4f", onSelectTask)}
      {renderSection("No Worker", data.no_worker, "#faad14", onSelectTask)}
      {renderSection("Expired / Cancelled", data.expired, "#faad14", onSelectTask)}
      {renderSection("Recovery", data.recovery, "#1677ff", onSelectTask)}
      {renderSection("Cleanup", data.cleanup, "#888", onSelectTask)}
    </div>
  );
}

function renderSection(
  label: string,
  section: { label: string; items: ProjectionRow[] },
  accent: string,
  onSelect?: (id: string) => void,
) {
  if (!section.items || section.items.length === 0) return null;

  return (
    <section key={label} aria-label={label} style={{ marginBottom: 20 }}>
      <h3 style={{ fontSize: 14, fontWeight: 600, color: accent, marginBottom: 8 }}>{label}</h3>
      <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        {section.items.map((row) => (
          <button
            key={row.task_id}
            onClick={() => onSelect?.(row.task_id)}
            style={{
              display: "grid",
              gridTemplateColumns: "160px 100px 1fr auto",
              gap: 12,
              padding: "8px 12px",
              background: "#0a0a0a",
              border: "1px solid #1a1a1a",
              borderRadius: 4,
              cursor: "pointer",
              textAlign: "left",
              fontSize: 12,
              color: "#aaa",
            }}
            aria-label={`${row.task_type} task ${row.task_id}, ${row.normalized_status}`}
          >
            <span style={{ color: "#1677ff", fontFamily: "monospace", fontSize: 11 }}>{row.task_id}</span>
            <span>{row.task_type}</span>
            <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{row.terminal_reason || `${row.normalized_status}`}</span>
            <span style={{ color: "#666" }}>{row.updated_at ? new Date(row.updated_at).toLocaleTimeString() : "-"}</span>
          </button>
        ))}
      </div>
    </section>
  );
}
