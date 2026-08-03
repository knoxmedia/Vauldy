import { useEffect, useState, useCallback } from "react";
import { TaskTypeNav } from "../components/tasks/TaskTypeNav";
import { TaskList } from "../components/tasks/TaskList";
import { TaskFilters } from "../components/tasks/TaskFilters";
import { TaskOverview } from "../components/tasks/TaskOverview";
import { TaskDetailDrawer } from "../components/tasks/TaskDetailDrawer";
import { fetchTaskControlRegistry } from "../api/taskControl";
import type { Registry } from "../api/taskControl";
import type { TaskControlFilter } from "../lib/taskControlFilters";
import { useT } from "../i18n";

export default function TaskManagerPage() {
  const t = useT();
  const [registry, setRegistry] = useState<Registry | null>(null);
  const [registryLoading, setRegistryLoading] = useState(true);
  const [registryError, setRegistryError] = useState(false);

  const [activeType, setActiveType] = useState("overview");
  const [filter, setFilter] = useState<TaskControlFilter>({});
  const [selectedTaskId, setSelectedTaskId] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setRegistryLoading(true);
    setRegistryError(false);

    fetchTaskControlRegistry()
      .then((reg) => {
        if (!cancelled) {
          setRegistry(reg);
          setRegistryLoading(false);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setRegistryError(true);
          setRegistryLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, []);

  const handleTypeSelect = useCallback((type: string) => {
    setActiveType(type);
    setFilter({});
    setSelectedTaskId(null);
  }, []);

  const handleFilterChange = useCallback((newFilter: TaskControlFilter) => {
    setFilter(newFilter);
  }, []);

  const handleSelectRow = useCallback((taskId: string) => {
    setSelectedTaskId(taskId);
  }, []);

  const handleCloseDetail = useCallback(() => {
    setSelectedTaskId(null);
  }, []);

  const handleDrillDownType = useCallback((taskType: string) => {
    setActiveType(taskType);
    setSelectedTaskId(null);
  }, []);

  const allTypeKeys = registry
    ? [
        "overview",
        ...registry.groups.flatMap((g) => g.types.map((x) => x.type)),
      ]
    : [];

  const validActiveType = allTypeKeys.includes(activeType) ? activeType : "overview";

  return (
    <main role="main" aria-label={t("pages.task_manager.page_title")}>
      <h1 style={{ fontSize: 20, fontWeight: 600, marginBottom: 16, color: "#d9d9d9" }}>
        {t("pages.task_manager.page_title")}
      </h1>

      {registryLoading && (
        <div style={{ padding: 40, textAlign: "center", color: "#888" }}>
          Loading registry...
        </div>
      )}

      {registryError && (
        <div style={{ padding: 40, textAlign: "center" }}>
          <p style={{ color: "#ff4d4f" }}>Failed to load task registry</p>
          <button
            onClick={() => {
              setRegistryError(false);
              setRegistryLoading(true);
              fetchTaskControlRegistry()
                .then(setRegistry)
                .catch(() => setRegistryError(true))
                .finally(() => setRegistryLoading(false));
            }}
            style={{
              padding: "6px 16px",
              background: "#1677ff",
              color: "#fff",
              border: "none",
              borderRadius: 4,
              cursor: "pointer",
            }}
          >
            Retry
          </button>
        </div>
      )}

      {registry && !registryLoading && (
        <>
          <TaskTypeNav
            registry={registry}
            activeType={validActiveType}
            onSelect={handleTypeSelect}
          />

          {validActiveType === "overview" ? (
            <div
              role="tabpanel"
              id="task-panel-overview"
              aria-labelledby="task-tab-overview"
            >
              <TaskOverview
                onDrillDownType={handleDrillDownType}
                onSelectTask={handleSelectRow}
              />
            </div>
          ) : (
            <div
              role="tabpanel"
              id={`task-panel-${validActiveType}`}
              aria-labelledby={`task-tab-${validActiveType}`}
            >
              <TaskFilters filter={filter} onChange={handleFilterChange} />
              <TaskList
                key={`${validActiveType}-${JSON.stringify(filter)}`}
                taskType={validActiveType}
                filter={filter}
                onSelectRow={handleSelectRow}
              />
            </div>
          )}

          <TaskDetailDrawer
            taskId={selectedTaskId}
            onClose={handleCloseDetail}
          />
        </>
      )}
    </main>
  );
}
