import { useCallback, useMemo } from "react";
import type { Registry } from "../../api/taskControl";
import { useT, tGlobal } from "../../i18n";

export interface TaskTypeNavProps {
  registry: Registry;
  activeType: string;
  onSelect: (type: string) => void;
  /** Per-type task counts (removed excluded). Types with a zero count are hidden. */
  typeCounts?: Record<string, number>;
}

interface FlatTabItem {
  key: string;
  label: string;
  groupLabel?: string;
  available: boolean;
}

function formatTypeName(type: string): string {
  return type
    .split("_")
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

function flattenTabs(registry: Registry, typeCounts: Record<string, number> | undefined, t: (k: string) => string): FlatTabItem[] {
  const tabs: FlatTabItem[] = [];
  tabs.push({ key: "overview", label: t("tasks.control.tab_overview"), available: true });

  for (const group of registry.groups) {
    if (!group.types || group.types.length === 0) continue;
    for (const spec of group.types) {
      if (typeCounts && typeCounts[spec.type] === 0) continue;
      tabs.push({
        key: spec.type,
        label: formatTypeName(spec.type),
        groupLabel: group.label,
        available: spec.available,
      });
    }
  }

  return tabs;
}

export function TaskTypeNav({ registry, activeType, onSelect, typeCounts }: TaskTypeNavProps) {
  const t = useT();
  const tabs = useMemo(() => flattenTabs(registry, typeCounts, tGlobal), [registry, typeCounts]);

  const activeIndex = tabs.findIndex((tab) => tab.key === activeType);
  const safeIndex = activeIndex >= 0 ? activeIndex : 0;

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      let nextIdx = safeIndex;
      switch (e.key) {
        case "ArrowRight":
          nextIdx = Math.min(safeIndex + 1, tabs.length - 1);
          break;
        case "ArrowLeft":
          nextIdx = Math.max(safeIndex - 1, 0);
          break;
        case "Home":
          nextIdx = 0;
          break;
        case "End":
          nextIdx = tabs.length - 1;
          break;
        default:
          return;
      }
      e.preventDefault();
      if (nextIdx !== safeIndex) {
        onSelect(tabs[nextIdx]!.key);
      }
    },
    [safeIndex, tabs, onSelect],
  );

  if (tabs.length === 0) {
    return (
      <div role="tablist" aria-label="Task types">
        <div role="tab" aria-selected={false}>
          {t("tasks.control.no_tasks")}
        </div>
      </div>
    );
  }

  return (
    <nav
      role="tablist"
      aria-label={t("tasks.control.page_title")}
      onKeyDown={handleKeyDown}
      style={{
        display: "flex",
        overflowX: "auto",
        gap: 0,
        borderBottom: "1px solid #303030",
        marginBottom: 16,
      }}
    >
      {tabs.map((tab, idx) => {
        const isActive = idx === safeIndex;
        return (
          <button
            key={tab.key}
            role="tab"
            id={`task-tab-${tab.key}`}
            aria-selected={isActive}
            aria-controls={`task-panel-${tab.key}`}
            tabIndex={isActive ? 0 : -1}
            disabled={!tab.available}
            onClick={() => onSelect(tab.key)}
            style={{
              flex: "0 0 auto",
              padding: "8px 16px",
              border: "none",
              borderBottom: isActive ? "2px solid #1677ff" : "2px solid transparent",
              background: "transparent",
              color: isActive ? "#1677ff" : tab.available ? "#d9d9d9" : "#666",
              cursor: tab.available ? "pointer" : "not-allowed",
              fontSize: 14,
              fontFamily: "inherit",
              whiteSpace: "nowrap",
              transition: "border-color 0.2s, color 0.2s",
            }}
            title={tab.groupLabel ? `${tab.groupLabel} · ${tab.label}` : tab.label}
          >
            {tab.label}
          </button>
        );
      })}
    </nav>
  );
}
