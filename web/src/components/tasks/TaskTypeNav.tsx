import { useCallback, useMemo } from "react";
import type { Registry } from "../../api/taskControl";

export interface TaskTypeNavProps {
  registry: Registry;
  activeType: string;
  onSelect: (type: string) => void;
}

interface FlatTabItem {
  key: string;
  label: string;
  groupLabel?: string;
  available: boolean;
}

function flattenTabs(registry: Registry): FlatTabItem[] {
  const tabs: FlatTabItem[] = [];

  // Overview is always first
  tabs.push({ key: "overview", label: "Overview", available: true });

  for (const group of registry.groups) {
    if (group.types.length === 0) continue;
    // Add a separator for the group
    // Then add each type as a tab
    for (const spec of group.types) {
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

function formatTypeName(type: string): string {
  return type
    .split("_")
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

export function TaskTypeNav({ registry, activeType, onSelect }: TaskTypeNavProps) {
  const tabs = useMemo(() => flattenTabs(registry), [registry]);

  const activeIndex = tabs.findIndex((t) => t.key === activeType);
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
          No task types available
        </div>
      </div>
    );
  }

  return (
    <nav
      role="tablist"
      aria-label="Task type navigation"
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
            title={tab.groupLabel ? `${tab.groupLabel}: ${tab.label}` : tab.label}
          >
            {tab.label}
          </button>
        );
      })}
    </nav>
  );
}
