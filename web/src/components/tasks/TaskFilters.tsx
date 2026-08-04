import { useCallback } from "react";
import { Select, Space } from "antd";
import type { TaskControlFilter } from "../../lib/taskControlFilters";
import { useT } from "../../i18n";

export interface TaskFiltersProps {
  filter: TaskControlFilter;
  onChange: (filter: TaskControlFilter) => void;
}

const STATUS_OPTIONS = [
  { value: "", label_key: "all_statuses" },
  { value: "waiting", label_key: "status_waiting" },
  { value: "running", label_key: "status_running" },
  { value: "done", label_key: "status_done" },
  { value: "failed", label_key: "status_failed" },
  { value: "cancelled", label_key: "status_cancelled" },
  { value: "skipped", label_key: "status_skipped" },
] as const;

const REMOVED_OPTIONS = [
  { value: "exclude", label_key: "active_only" },
  { value: "include", label_key: "include_removed" },
  { value: "only", label_key: "removed_only" },
] as const;

export function TaskFilters({ filter, onChange }: TaskFiltersProps) {
  const t = useT();

  const handleStatusChange = useCallback(
    (value: string) => {
      onChange({ ...filter, status: value || undefined });
    },
    [filter, onChange],
  );

  const handleRemovedChange = useCallback(
    (value: string) => {
      onChange({ ...filter, removed: value || undefined });
    },
    [filter, onChange],
  );

  return (
    <Space size={8} style={{ marginBottom: 12 }}>
      <Select
        value={filter.status ?? ""}
        onChange={handleStatusChange}
        style={{ width: 140 }}
        size="small"
        options={[
          ...STATUS_OPTIONS.map((o) => ({ value: o.value, label: t(`tasks.control.${o.label_key}`) })),
        ]}
      />
      <Select
        value={filter.removed ?? "exclude"}
        onChange={handleRemovedChange}
        style={{ width: 160 }}
        size="small"
        options={[
          ...REMOVED_OPTIONS.map((o) => ({ value: o.value, label: t(`tasks.control.${o.label_key}`) })),
        ]}
      />
    </Space>
  );
}
