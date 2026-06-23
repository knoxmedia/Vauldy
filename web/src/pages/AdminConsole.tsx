import { Button, Card, Col, Progress, Row, Space, Statistic, Table, Tag, Tooltip } from "antd";
import { useEffect, useState } from "react";
import { ReloadOutlined } from "@ant-design/icons";
import { fetchAdminOverview, type AdminOverview } from "../api/client";
import { renderServerDateTime } from "../lib/datetime";
import { useT } from "../i18n";
import { useAuthStore } from "../store/auth";

export default function AdminConsolePage() {
  const t = useT();
  const token = useAuthStore((s) => s.token);
  const [overview, setOverview] = useState<AdminOverview | null>(null);
  const [overviewLoading, setOverviewLoading] = useState(false);
  const [streamConnected, setStreamConnected] = useState(false);

  const loadOverview = async (cancelled = false, silent = false) => {
    if (!silent) setOverviewLoading(true);
    try {
      const data = await fetchAdminOverview();
      if (!cancelled) setOverview(data);
    } catch {
      if (!cancelled) setOverview(null);
    } finally {
      if (!cancelled && !silent) setOverviewLoading(false);
    }
  };

  useEffect(() => {
    let cancelled = false;
    void loadOverview(cancelled, false);
    const timer = window.setInterval(() => {
      void loadOverview(false, true);
    }, 15000);
    let es: EventSource | null = null;
    if (token) {
      const url = `/api/v1/admin/overview/stream?access_token=${encodeURIComponent(token)}`;
      es = new EventSource(url);
      es.addEventListener("overview", (evt) => {
        try {
          const data = JSON.parse((evt as MessageEvent).data) as AdminOverview;
          if (!cancelled) {
            setOverview(data);
            setStreamConnected(true);
          }
        } catch {
          // ignore malformed event
        }
      });
      es.onerror = () => {
        if (!cancelled) setStreamConnected(false);
      };
    } else {
      setStreamConnected(false);
    }
    return () => {
      cancelled = true;
      window.clearInterval(timer);
      if (es) es.close();
    };
  }, [token]);

  const fmtBytesGB = (bytes?: number) => {
    if (!bytes || bytes <= 0) return "-";
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
  };

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <Card
        title={t("pages.admin_console.system_monitor")}
        loading={overviewLoading}
        extra={
          <Space>
            <Tag color={streamConnected ? "green" : "orange"}>
              {streamConnected ? t("pages.admin_console.stream_connected") : t("pages.admin_console.polling_mode")}
            </Tag>
            <Tooltip title={t("pages.admin_console.refresh_tooltip")}>
              <Button
                icon={<ReloadOutlined />}
                onClick={() => void loadOverview(false, false)}
                aria-label={t("pages.admin_console.refresh_aria")}
              />
            </Tooltip>
          </Space>
        }
      >
        <Row gutter={16}>
          <Col xs={24} md={12} lg={8}>
            <Card size="small">
              <Statistic title={t("pages.admin_console.cpu_usage")} value={overview?.monitor.cpu_percent ?? 0} precision={1} suffix="%" />
              <Progress percent={Number((overview?.monitor.cpu_percent ?? 0).toFixed(1))} size="small" />
            </Card>
          </Col>
          <Col xs={24} md={12} lg={8}>
            <Card size="small">
              <Statistic title={t("pages.admin_console.memory_usage")} value={overview?.monitor.memory_percent ?? 0} precision={1} suffix="%" />
              <Progress percent={Number((overview?.monitor.memory_percent ?? 0).toFixed(1))} size="small" />
            </Card>
          </Col>
          <Col xs={24} md={12} lg={8}>
            <Card size="small">
              <Statistic title={t("pages.admin_console.disk_usage")} value={overview?.monitor.disk_percent ?? 0} precision={1} suffix="%" />
              <Progress percent={Number((overview?.monitor.disk_percent ?? 0).toFixed(1))} size="small" />
            </Card>
          </Col>
          <Col xs={24} md={12} lg={8}>
            <Card size="small">
              <Statistic title={t("pages.admin_console.transcode_tasks")} value={overview?.monitor.transcode_task_count ?? 0} />
            </Card>
          </Col>
          <Col xs={24} md={12} lg={8}>
            <Card size="small">
              <Statistic title={t("pages.admin_console.media_total")} value={overview?.monitor.media_total ?? 0} />
            </Card>
          </Col>
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

      <Card title={t("pages.admin_console.current_activities")} loading={overviewLoading}>
        <Table
          rowKey="id"
          pagination={{ pageSize: 10 }}
          dataSource={overview?.activities ?? []}
          columns={[
            { title: t("pages.admin_console.col_time"), dataIndex: "created_at", width: 180, render: renderServerDateTime },
            { title: t("pages.admin_console.col_user"), dataIndex: "username", width: 120, render: (v?: string) => v || "-" },
            { title: t("pages.admin_console.col_action"), dataIndex: "action", width: 120 },
            { title: t("pages.admin_console.col_media_id"), dataIndex: "media_id", width: 100, render: (v: number) => (v > 0 ? v : "-") },
            { title: t("pages.admin_console.col_message"), dataIndex: "message", ellipsis: true },
          ]}
        />
      </Card>
    </Space>
  );
}
