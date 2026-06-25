import { Checkbox } from "antd";
import { HolderOutlined } from "@ant-design/icons";
import { useEffect, useMemo, useState } from "react";
import type { ScrapeProviderOption } from "../lib/scrapeProviders";
import { tGlobal } from "../i18n";
import styles from "./ProviderPriorityPicker.module.css";

type RowState = { id: string; enabled: boolean };

export type ProviderPriorityPickerProps = {
  value?: string[];
  onChange?: (value: string[]) => void;
  options: ScrapeProviderOption[];
  hint?: string;
};

function buildRows(value: string[] | undefined, catalog: ScrapeProviderOption[]): RowState[] {
  const catalogIds = catalog.map((o) => o.value);
  const saved = (value ?? []).filter((id) => catalogIds.includes(id));
  const enabledSet = new Set(saved);
  const rest = catalogIds.filter((id) => !saved.includes(id));
  return [...saved, ...rest].map((id) => ({ id, enabled: enabledSet.has(id) }));
}

function rowsToValue(rows: RowState[]): string[] {
  return rows.filter((r) => r.enabled).map((r) => r.id);
}

export default function ProviderPriorityPicker({
  value,
  onChange,
  options,
  hint,
}: ProviderPriorityPickerProps) {
  const valueKey = (value ?? []).join("\0");
  const [rows, setRows] = useState(() => buildRows(value, options));
  const [dragIndex, setDragIndex] = useState<number | null>(null);
  const [dropIndex, setDropIndex] = useState<number | null>(null);

  useEffect(() => {
    setRows(buildRows(value, options));
  }, [valueKey, options]);

  const optionById = useMemo(() => new Map(options.map((o) => [o.value, o])), [options]);

  function commit(next: RowState[]) {
    setRows(next);
    onChange?.(rowsToValue(next));
  }

  function toggleAt(index: number) {
    const next = rows.map((row, i) => (i === index ? { ...row, enabled: !row.enabled } : row));
    commit(next);
  }

  function reorder(from: number, to: number) {
    if (from === to || from < 0 || to < 0 || from >= rows.length || to >= rows.length) return;
    const next = [...rows];
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    commit(next);
  }

  return (
    <div className={styles.wrap}>
      {hint ? <p className={styles.hint}>{hint}</p> : null}
      <div className={styles.list} role="list">
        {rows.map((row, index) => {
          const opt = optionById.get(row.id);
          if (!opt) return null;
          return (
            <div
              key={row.id}
              role="listitem"
              className={`${styles.row} ${dragIndex === index ? styles.rowDragging : ""} ${
                dropIndex === index && dragIndex !== index ? styles.rowDropTarget : ""
              }`}
              onDragOver={(e) => {
                if (dragIndex == null) return;
                e.preventDefault();
                e.dataTransfer.dropEffect = "move";
                if (index !== dragIndex) setDropIndex(index);
              }}
              onDragLeave={(e) => {
                if (!e.currentTarget.contains(e.relatedTarget as Node)) {
                  setDropIndex((cur) => (cur === index ? null : cur));
                }
              }}
              onDrop={(e) => {
                e.preventDefault();
                const from = Number.parseInt(e.dataTransfer.getData("text/plain"), 10);
                if (Number.isFinite(from)) reorder(from, index);
                setDragIndex(null);
                setDropIndex(null);
              }}
            >
              <Checkbox className={styles.checkbox} checked={row.enabled} onChange={() => toggleAt(index)} />
              <div className={styles.body}>
                <div className={styles.label}>{opt.label}</div>
                {opt.description ? <div className={styles.description}>{opt.description}</div> : null}
              </div>
              <span
                className={styles.dragHandle}
                draggable
                title={tGlobal("pages.playlists.drag_to_sort")}
                onDragStart={(e) => {
                  e.dataTransfer.setData("text/plain", String(index));
                  e.dataTransfer.effectAllowed = "move";
                  setDragIndex(index);
                }}
                onDragEnd={() => {
                  setDragIndex(null);
                  setDropIndex(null);
                }}
              >
                <HolderOutlined />
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
