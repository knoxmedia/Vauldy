import { Alert, Button, Card, Col, Empty, Progress, Row, Space, Statistic, Table, Tag, Tooltip } from "antd";
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

export function isAdminOverview(value: unknown): value is AdminOverview {
  if (!isRecord(value) || !isRecord(value.monitor) || !isRecord(value.system) || !Array.isArray(value.activities)) {
    return false;
  }
  const monitorNumbers = ["cpu_percent", "memory_percent", "disk_percent", "transcode_task_count", "media_total"];
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
  const queue = value.post_ingest_queue;
  const budget = value.resource_budget;
  const metrics = value.sqlite_metrics;
  if (!isRecord(queue) || !isRecord(queue.by_status) || !isRecord(queue.by_type)
    || typeof queue.oldest_waiting_seconds !== "number" || typeof queue.expired_lease_count !== "number"
    || !Object.values(queue.by_status).every((item) => typeof item === "number")
    || !Object.values(queue.by_type).every((item) => isRecord(item) && Object.values(item).every((count) => typeof count === "number"))) return false;
  if (!Array.isArray(value.running_post_ingest_tasks) || !Array.isArray(value.scan_leases) || !isRecord(budget) || !isRecord(metrics)) return false;
  const runningNumbers = ["id", "media_id", "attempts", "attempt", "max_attempts", "run_seconds"];
  const runningStrings = ["task_type", "type", "started_at", "lease_owner", "lease_until", "lease_expires"];
  if (!value.running_post_ingest_tasks.every((task) => isRecord(task)
    && runningNumbers.every((key) => typeof task[key] === "number")
    && runningStrings.every((key) => typeof task[key] === "string")
    && (task.scan_task_id === null || typeof task.scan_task_id === "number"))) return false;
  if (!value.scan_leases.every((lease) => isRecord(lease)
    && ["library_id", "scan_task_id"].every((key) => typeof lease[key] === "number")
    && ["owner_id", "lease_until"].every((key) => typeof lease[key] === "string")
    && typeof lease.expired === "boolean")) return false;
  if (!["global_limit", "global_used", "poster_limit", "poster_used", "preview_limit", "preview_used"].every((key) => typeof budget[key] === "number")) return false;
  if (!(typeof metrics.scope === "string" && metrics.persistent === false
    && ["busy_retries", "busy_exhausted", "progress_batches", "log_batches", "log_failures", "dropped_logs"].every((key) => typeof metrics[key] === "number"))) return false;
  if (!Array.isArray(value.publication_policy)) return false;
  const policyNumbers = ["media_id", "run_id", "generation", "policy_version", "required_waiting", "required_failed", "optional_waiting", "optional_failed"];
  const policyStrings = ["status", "terminal_reason", "recovery_error"];
  return value.publication_policy.every((row) => isRecord(row)
    && policyNumbers.every((key) => typeof row[key] === "number")
    && policyStrings.every((key) => typeof row[key] === "string")
    && Array.isArray(row.adapter_unavailable) && row.adapter_unavailable.every((item) => typeof item === "string")
    && Array.isArray(row.metadata_errors) && row.metadata_errors.every((item) => typeof item === "string"));
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
          if (!isAdminOverview(parsed) || !mountedRef.current || !owner.active || generationRef.current !== owner) return;
          owner.hasFirstData = true;
          setOverview(parsed);
          setOverviewLoading(false);
          setOverviewError(false);
          setStreamConnected(true);
          if (requestRef.current?.generation === owner.id) requestRef.current.controller.abort();
        } catch {
          // Ignore malformed events and keep waiting for REST or a valid SSE snapshot.
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
          <Col xs={24} md={12} lg={8}><Card size="small"><Statistic title={t("pages.admin_console.transcode_tasks")} value={overview?.monitor.transcode_task_count ?? 0} /></Card></Col>
          <Col xs={24} md={12} lg={8}><Card size="small"><Statistic title={t("pages.admin_console.media_total")} value={overview?.monitor.media_total ?? 0} /></Card></Col>
        </Row>
      </Card>
      <Card title={t("pages.admin_console.system_info")} loading={overviewLoading}>
        <Row gutter={16}>
          <Col xs={24} md={12} lg={8}><Statistic title={t("pages.admin_console.cpu_count")} value={overview?.system.cpu_count ?? 0} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title={t("pages.admin_console.memory_size")} value={fmtBytesGB(overview?.system.memory_total)} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title={t("pages.admin_console.os")} value={overview?.system.os || "-"} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title={t("pages.admin_console.database")} value={overview?.system.database || "-"} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title={t("pages.admin_console.software_version")} value={overview?.system.software_version || "dev"} /></Col>
        </Row>
      </Card>

      <Card title={t("pages.admin_console.resource_control")} loading={overviewLoading}>
        <Space direction="vertical" size="large" style={{ width: "100%" }}>
          <Row gutter={[16, 16]}>
            <Col xs={24} lg={12}>
              <Card size="small" title={t("pages.admin_console.queue_by_status")}>
                {overview && Object.keys(overview.post_ingest_queue.by_status).length ? (
                  <Space wrap>{Object.entries(overview.post_ingest_queue.by_status).sort(([a], [b]) => a.localeCompare(b)).map(([status, count]) => <Tag key={status}>{status}: {count}</Tag>)}</Space>
                ) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />}
              </Card>
            </Col>
            <Col xs={24} lg={12}>
              <Card size="small" title={t("pages.admin_console.queue_by_type")}>
                {overview && Object.keys(overview.post_ingest_queue.by_type).length ? (
                  <Space wrap>{Object.entries(overview.post_ingest_queue.by_type).sort(([a], [b]) => a.localeCompare(b)).flatMap(([type, statuses]) => Object.entries(statuses).sort(([a], [b]) => a.localeCompare(b)).map(([status, count]) => <Tag key={`${type}-${status}`}>{type} / {status}: {count}</Tag>))}</Space>
                ) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />}
              </Card>
            </Col>
          </Row>
          <Row gutter={[16, 16]}>
            <Col xs={24} md={12}><Statistic title={t("pages.admin_console.oldest_waiting_seconds")} value={overview?.post_ingest_queue.oldest_waiting_seconds ?? 0} suffix={t("pages.admin_console.seconds")} /></Col>
            <Col xs={24} md={12}><Statistic title={t("pages.admin_console.expired_lease_count")} value={overview?.post_ingest_queue.expired_lease_count ?? 0} /></Col>
          </Row>
          <Card size="small" title={t("pages.admin_console.running_post_ingest_tasks")}>
            <Table rowKey="id" size="small" pagination={false} scroll={{ x: 1200 }} dataSource={(overview?.running_post_ingest_tasks ?? []).slice(0, 50)} columns={[
              { title: t("pages.admin_console.col_task_id"), dataIndex: "id" },
              { title: t("pages.admin_console.col_media_id"), dataIndex: "media_id" },
              { title: t("pages.admin_console.col_task_type"), dataIndex: "task_type" },
              { title: t("pages.admin_console.col_scan_task_id"), dataIndex: "scan_task_id", render: (value: number | null) => value ?? "-" },
              { title: t("pages.admin_console.col_attempt"), render: (_, row) => `${row.attempts} / ${row.max_attempts}` },
              { title: t("pages.admin_console.col_run_seconds"), dataIndex: "run_seconds" },
              { title: t("pages.admin_console.col_started_at"), dataIndex: "started_at", render: renderServerDateTime },
              { title: t("pages.admin_console.col_lease_owner"), dataIndex: "lease_owner" },
              { title: t("pages.admin_console.col_lease_until"), dataIndex: "lease_until", render: renderServerDateTime },
            ]} />
          </Card>
          <Card size="small" title={t("pages.admin_console.scan_leases")}>
            <Table rowKey="library_id" size="small" pagination={false} scroll={{ x: 700 }} dataSource={overview?.scan_leases ?? []} columns={[
              { title: t("pages.admin_console.col_library_id"), dataIndex: "library_id" },
              { title: t("pages.admin_console.col_scan_task_id"), dataIndex: "scan_task_id" },
              { title: t("pages.admin_console.col_owner_id"), dataIndex: "owner_id" },
              { title: t("pages.admin_console.col_lease_until"), dataIndex: "lease_until", render: renderServerDateTime },
              { title: t("pages.admin_console.col_status"), dataIndex: "expired", render: (expired: boolean) => <Tag color={expired ? "red" : "green"}>{expired ? t("pages.admin_console.expired") : t("pages.admin_console.active")}</Tag> },
            ]} />
          </Card>
          <Row gutter={[16, 16]}>
            {(["global", "poster", "preview"] as const).map((kind) => <Col xs={24} md={8} key={kind}><Card size="small"><Statistic title={t(`pages.admin_console.budget_${kind}`)} value={`${overview?.resource_budget[`${kind}_used`] ?? 0} / ${overview?.resource_budget[`${kind}_limit`] ?? 0}`} /></Card></Col>)}
          </Row>
          <Card size="small" title={t("pages.admin_console.sqlite_metrics")} extra={<Tag color="blue">{t("pages.admin_console.sqlite_process_scope")}</Tag>}>
            <Row gutter={[16, 16]}>
              {(["busy_retries", "busy_exhausted", "progress_batches", "log_batches", "log_failures", "dropped_logs"] as const).map((metric) => <Col xs={12} md={8} key={metric}><Statistic title={t(`pages.admin_console.${metric}`)} value={overview?.sqlite_metrics[metric] ?? 0} /></Col>)}
            </Row>
          </Card>
        </Space>
      </Card>
      <Card title={t("pages.admin_console.publication_policy")} loading={overviewLoading}>
        <Table
          rowKey="run_id"
          size="small"
          pagination={false}
          scroll={{ x: 1400 }}
          dataSource={(overview?.publication_policy ?? []).slice(0, 100)}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /> }}
          columns={[
            { title: t("pages.admin_console.col_media_id"), dataIndex: "media_id" },
            { title: t("pages.admin_console.col_policy_version"), dataIndex: "policy_version" },
            { title: t("pages.admin_console.col_generation"), dataIndex: "generation" },
            { title: t("pages.admin_console.col_status"), dataIndex: "status" },
            { title: t("pages.admin_console.col_terminal_reason"), dataIndex: "terminal_reason", render: (value: string) => value || "-" },
            { title: t("pages.admin_console.col_required_counts"), render: (_, row) => `${row.required_waiting} / ${row.required_failed}` },
            { title: t("pages.admin_console.col_optional_counts"), render: (_, row) => `${row.optional_waiting} / ${row.optional_failed}` },
            { title: t("pages.admin_console.col_adapter_unavailable"), dataIndex: "adapter_unavailable", render: (value: string[]) => (value?.length ? value.join(", ") : "-") },
            { title: t("pages.admin_console.col_metadata_errors"), dataIndex: "metadata_errors", render: (value: string[]) => (value?.length ? value.join("; ") : "-"), ellipsis: true },
            { title: t("pages.admin_console.col_recovery_error"), dataIndex: "recovery_error", render: (value: string) => value || "-", ellipsis: true },
          ]}
        />
      </Card>
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
