import { useEffect, useState, useCallback } from "react";
import { Button, Alert } from "antd";
import { TaskTypeNav } from "../components/tasks/TaskTypeNav";
import { TaskList } from "../components/tasks/TaskList";
import { TaskOverview } from "../components/tasks/TaskOverview";
import { TaskDetailDrawer } from "../components/tasks/TaskDetailDrawer";
import { fetchTaskControlRegistry } from "../api/taskControl";
import type { Registry } from "../api/taskControl";
import { useT } from "../i18n";

export default function TaskManagerPage() {
  const t = useT();
  const [registry, setRegistry] = useState<Registry | null>(null);
  const [registryLoading, setRegistryLoading] = useState(true);
  const [registryError, setRegistryError] = useState(false);

  const [activeType, setActiveType] = useState("overview");
  const [selectedTaskId, setSelectedTaskId] = useState<string | null>(null);

  const [refreshKey, setRefreshKey] = useState(0);

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
    setSelectedTaskId(null);
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

  const handleActionSuccess = useCallback(() => {
    setRefreshKey((k) => k + 1);
  }, []);

  const allTypeKeys = registry
    ? [
        "overview",
        ...registry.groups.flatMap((g) => (g.types ?? []).map((x) => x.type)),
      ]
    : [];

  const validActiveType = allTypeKeys.includes(activeType) ? activeType : "overview";

  return (
    <main role="main" aria-label={t("tasks.control.page_title")}>
      <h1 style={{ fontSize: 20, fontWeight: 600, marginBottom: 16, color: "#d9d9d9" }}>
        {t("tasks.control.page_title")}
      </h1>

      {registryLoading && (
        <div style={{ padding: 40, textAlign: "center", color: "#888" }}>
          {t("tasks.control.loading_registry")}
        </div>
      )}

      {registryError && (
        <Alert
          type="error"
          showIcon
          message={t("tasks.control.load_failed_registry")}
          style={{ marginBottom: 16 }}
          action={
            <Button size="small" onClick={() => {
              setRegistryError(false);
              setRegistryLoading(true);
              fetchTaskControlRegistry()
                .then(setRegistry)
                .catch(() => setRegistryError(true))
                .finally(() => setRegistryLoading(false));
            }}>
              {t("tasks.control.retry")}
            </Button>
          }
        />
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
              <TaskList
                key={`${validActiveType}-${refreshKey}`}
                taskType={validActiveType}
                onSelectRow={handleSelectRow}
                onActionSuccess={handleActionSuccess}
              />
            </div>
          )}

          <TaskDetailDrawer
            taskId={selectedTaskId}
            onClose={handleCloseDetail}
            onActionSuccess={handleActionSuccess}
          />
        </>
      )}
    </main>
  );
}
