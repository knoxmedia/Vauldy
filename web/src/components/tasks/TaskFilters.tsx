import { useCallback } from "react";
import type { TaskControlFilter } from "../../lib/taskControlFilters";

export interface TaskFiltersProps {
  filter: TaskControlFilter;
  onChange: (filter: TaskControlFilter) => void;
}

const STATUS_OPTIONS = [
  { value: "", label: "All statuses" },
  { value: "waiting", label: "Waiting" },
  { value: "running", label: "Running" },
  { value: "done", label: "Done" },
  { value: "failed", label: "Failed" },
  { value: "cancelled", label: "Cancelled" },
  { value: "skipped", label: "Skipped" },
];

const REMOVED_OPTIONS = [
  { value: "exclude", label: "Active only" },
  { value: "include", label: "Include removed" },
  { value: "only", label: "Removed only" },
];

export function TaskFilters({ filter, onChange }: TaskFiltersProps) {
  const handleStatusChange = useCallback(
    (e: React.ChangeEvent<HTMLSelectElement>) => {
      onChange({ ...filter, status: e.target.value || undefined });
    },
    [filter, onChange],
  );

  const handleRemovedChange = useCallback(
    (e: React.ChangeEvent<HTMLSelectElement>) => {
      onChange({ ...filter, removed: e.target.value || undefined });
    },
    [filter, onChange],
  );

  return (
    <div
      style={{
        display: "flex",
        gap: 12,
        alignItems: "center",
        marginBottom: 16,
        flexWrap: "wrap",
      }}
      role="group"
      aria-label="Task filters"
    >
      <label style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 13, color: "#aaa" }}>
        Status:
        <select
          value={filter.status ?? ""}
          onChange={handleStatusChange}
          style={{
            padding: "4px 8px",
            background: "#1a1a1a",
            color: "#d9d9d9",
            border: "1px solid #303030",
            borderRadius: 4,
            fontSize: 13,
          }}
          aria-label="Filter by status"
        >
          {STATUS_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>
      </label>

      <label style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 13, color: "#aaa" }}>
        Removed:
        <select
          value={filter.removed ?? "exclude"}
          onChange={handleRemovedChange}
          style={{
            padding: "4px 8px",
            background: "#1a1a1a",
            color: "#d9d9d9",
            border: "1px solid #303030",
            borderRadius: 4,
            fontSize: 13,
          }}
          aria-label="Filter by removed state"
        >
          {REMOVED_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>
      </label>
    </div>
  );
}
