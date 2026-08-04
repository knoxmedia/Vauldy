import { Alert, Button, Card, Col, Progress, Row, Space, Statistic, Table, Tag, Tooltip } from "antd";
import { useCallback, useEffect, useRef, useState } from "react";
import { ReloadOutlined } from "@ant-design/icons";
import { fetchAdminOverview, type AdminOverview } from "../api/client";
import { renderServerDateTime } from "../lib/datetime";
import { useT } from "../i18n";
import { useAuthStore } from "../store/auth";

type EffectGeneration = { id: number; active: boolean; hasFirstData: boolean };

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Console system-only validator: checks CPU, memory, disk, system info,
 * activities, and SQLite metrics. Task control fields (queue, leases,
 * budget, publication) are explicitly NOT validated here.
 */
export function isAdminOverviewSystemOnly(value: unknown): value is AdminOverview {
  if (!isRecord(value) || !isRecord(value.monitor) || !isRecord(value.system) || !Array.isArray(value.activities)) {
    return false;
  }
  const monitorNumbers = ["cpu_percent", "memory_percent", "disk_percent"];
  const systemNumbers = ["cpu_count", "memory_total"];
  const systemStrings = ["os", "database", "software_version"];
  const monitor = value.monitor;
  const system = value.system;
  if (!monitorNumbers.every((key) => typeof monitor[key] === "number")
    || !systemNumbers.every((key) => typeof system[key] === "number")
    || !systemStrings.every((key) => typeof system[key] === "string")) return false;
  const activityNumbers = ["id", "media_id"];
  const activityStrings = ["username", "action", "message", "created_at"];
  if (!value.activities.every((activity) => isRecord(activity)
    && activityNumbers.every((key) => typeof activity[key] === "number")
    && activityStrings.every((key) => typeof activity[key] === "string"))) return false;
  // SQLite metrics (system health, not task control)
  const metrics = value.sqlite_metrics;
  if (!isRecord(metrics)
    || !(typeof metrics.scope === "string" && metrics.persistent === false
    && ["busy_retries", "busy_exhausted", "progress_batches", "log_batches", "log_failures", "dropped_logs"].every((key) => typeof metrics[key] === "number"))) return false;
  // Reject payloads that contain task control fields (system-only console)
  const taskControlKeys = ["post_ingest_queue", "task_alignment", "running_post_ingest_tasks", "scan_leases", "resource_budget", "publication_policy", "transcode_task_count", "media_total"];
  for (const key of taskControlKeys) {
    if (key in value || (key in (value.monitor as Record<string, unknown>))) return false;
  }
  return true;
}

export default function AdminConsolePage() {
  const t = useT();
  const token = useAuthStore((s) => s.token);
  const [overview, setOverview] = useState<AdminOverview | null>(null);
  const [overviewLoading, setOverviewLoading] = useState(false);
  const [overviewError, setOverviewError] = useState(false);
  const [streamConnected, setStreamConnected] = useState(false);
  const mountedRef = useRef(false);
  const generationCounterRef = useRef(0);
  const generationRef = useRef<EffectGeneration | null>(null);
  const requestRef = useRef<{ generation: number; controller: AbortController } | null>(null);

  const loadOverview = useCallback(async (owner: EffectGeneration, silent = false) => {
    if (!mountedRef.current || !owner.active || generationRef.current !== owner) return;
    requestRef.current?.controller.abort();
    const controller = new AbortController();
    requestRef.current = { generation: owner.id, controller };
    if (!silent) {
      setOverviewLoading(true);
      setOverviewError(false);
    }
    try {
      const data = await fetchAdminOverview(controller.signal);
      if (mountedRef.current && owner.active && generationRef.current === owner && !controller.signal.aborted && !owner.hasFirstData) {
        setOverview(data);
        setOverviewError(false);
      }
    } catch (error) {
      const isAbort = controller.signal.aborted || (error instanceof DOMException && error.name === "AbortError");
      if (mountedRef.current && owner.active && generationRef.current === owner && !isAbort && !owner.hasFirstData) {
        setOverviewError(true);
      }
    } finally {
      if (mountedRef.current && owner.active && generationRef.current === owner && !silent && !controller.signal.aborted && !owner.hasFirstData) {
        setOverviewLoading(false);
      }
    }
  }, []);

  const retryOverview = useCallback(() => {
    const owner = generationRef.current;
    if (owner) void loadOverview(owner, false);
  }, [loadOverview]);

  useEffect(() => {
    mountedRef.current = true;
    const owner: EffectGeneration = { id: ++generationCounterRef.current, active: true, hasFirstData: false };
    generationRef.current = owner;
    void loadOverview(owner, false);
    const timer = window.setInterval(() => {
      if (!owner.hasFirstData) void loadOverview(owner, true);
    }, 15000);
    let es: EventSource | null = null;
    if (token) {
      const url = `/api/v1/admin/overview/stream?access_token=${encodeURIComponent(token)}`;
      es = new EventSource(url);
      es.addEventListener("overview", (evt) => {
        try {
          const parsed: unknown = JSON.parse((evt as MessageEvent).data);
          if (!isAdminOverviewSystemOnly(parsed) || !mountedRef.current || !owner.active || generationRef.current !== owner) return;
          owner.hasFirstData = true;
          setOverview(parsed);
          setOverviewLoading(false);
          setOverviewError(false);
          setStreamConnected(true);
          if (requestRef.current?.generation === owner.id) requestRef.current.controller.abort();
        } catch {
          // Ignore malformed events
        }
      });
      es.onerror = () => {
        if (mountedRef.current && owner.active && generationRef.current === owner) setStreamConnected(false);
      };
    } else {
      setStreamConnected(false);
    }
    return () => {
      owner.active = false;
      window.clearInterval(timer);
      es?.close();
      if (requestRef.current?.generation === owner.id) requestRef.current.controller.abort();
      if (generationRef.current === owner) {
        generationRef.current = null;
        mountedRef.current = false;
      }
    };
  }, [loadOverview, token]);

  const fmtBytesGB = (bytes?: number) => {
    if (!bytes || bytes <= 0) return "-";
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
  };

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      {overviewError && !overview ? (
        <Alert
          type="error"
          showIcon
          message={t("common.loading_failed")}
          action={<Button size="small" onClick={retryOverview}>{t("common.retry")}</Button>}
        />
      ) : null}

      {/* System Monitor: CPU, Memory, Disk only */}
      <Card
        title={t("pages.admin_console.system_monitor")}
        loading={overviewLoading}
        extra={
          <Space>
            <Tag color={streamConnected ? "green" : "orange"}>
              {streamConnected ? t("pages.admin_console.stream_connected") : t("pages.admin_console.polling_mode")}
            </Tag>
            <Tooltip title={t("pages.admin_console.refresh_tooltip")}>
              <Button icon={<ReloadOutlined />} onClick={retryOverview} aria-label={t("pages.admin_console.refresh_aria")} />
            </Tooltip>
          </Space>
        }
      >
        <Row gutter={16}>
          <Col xs={24} md={12} lg={8}><Card size="small"><Statistic title={t("pages.admin_console.cpu_usage")} value={overview?.monitor.cpu_percent ?? 0} precision={1} suffix="%" /><Progress percent={Number((overview?.monitor.cpu_percent ?? 0).toFixed(1))} size="small" /></Card></Col>
          <Col xs={24} md={12} lg={8}><Card size="small"><Statistic title={t("pages.admin_console.memory_usage")} value={overview?.monitor.memory_percent ?? 0} precision={1} suffix="%" /><Progress percent={Number((overview?.monitor.memory_percent ?? 0).toFixed(1))} size="small" /></Card></Col>
          <Col xs={24} md={12} lg={8}><Card size="small"><Statistic title={t("pages.admin_console.disk_usage")} value={overview?.monitor.disk_percent ?? 0} precision={1} suffix="%" /><Progress percent={Number((overview?.monitor.disk_percent ?? 0).toFixed(1))} size="small" /></Card></Col>
        </Row>
      </Card>

      {/* System Info: CPU count, memory, OS, database, version */}
      <Card title={t("pages.admin_console.system_info")} loading={overviewLoading}>
        <Row gutter={16}>
          <Col xs={24} md={12} lg={8}><Statistic title={t("pages.admin_console.cpu_count")} value={overview?.system.cpu_count ?? 0} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title={t("pages.admin_console.memory_size")} value={fmtBytesGB(overview?.system.memory_total)} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title={t("pages.admin_console.os")} value={overview?.system.os || "-"} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title={t("pages.admin_console.database")} value={overview?.system.database || "-"} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title={t("pages.admin_console.software_version")} value={overview?.system.software_version || "dev"} /></Col>
        </Row>
      </Card>

      {/* SQLite health (system scope only) */}
      <Card title={t("pages.admin_console.sqlite_metrics")} loading={overviewLoading} extra={<Tag color="blue">{t("pages.admin_console.sqlite_process_scope")}</Tag>}>
        <Row gutter={[16, 16]}>
          {(["busy_retries", "busy_exhausted", "progress_batches", "log_batches", "log_failures", "dropped_logs"] as const).map((metric) => <Col xs={12} md={8} key={metric}><Statistic title={t(`pages.admin_console.${metric}`)} value={overview?.sqlite_metrics[metric] ?? 0} /></Col>)}
        </Row>
      </Card>

      {/* Current Activities */}
      <Card title={t("pages.admin_console.current_activities")} loading={overviewLoading}>
        <Table rowKey="id" pagination={{ pageSize: 10 }} dataSource={overview?.activities ?? []} columns={[
          { title: t("pages.admin_console.col_time"), dataIndex: "created_at", width: 180, render: renderServerDateTime },
          { title: t("pages.admin_console.col_user"), dataIndex: "username", width: 120, render: (v?: string) => v || "-" },
          { title: t("pages.admin_console.col_action"), dataIndex: "action", width: 120 },
          { title: t("pages.admin_console.col_media_id"), dataIndex: "media_id", width: 100, render: (v: number) => (v > 0 ? v : "-") },
          { title: t("pages.admin_console.col_message"), dataIndex: "message", ellipsis: true },
        ]} />
      </Card>
    </Space>
  );
}
