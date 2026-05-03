import { Button, Card, Col, Progress, Row, Space, Statistic, Table, Tag, Tooltip } from "antd";
import { useEffect, useState } from "react";
import { ReloadOutlined } from "@ant-design/icons";
import { fetchAdminOverview, type AdminOverview } from "../api/client";
import { useAuthStore } from "../store/auth";

export default function AdminConsolePage() {
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
        title="系统监控"
        loading={overviewLoading}
        extra={
          <Space>
            <Tag color={streamConnected ? "green" : "orange"}>
              {streamConnected ? "推送已连接" : "轮询模式"}
            </Tag>
            <Tooltip title="刷新监控">
              <Button icon={<ReloadOutlined />} onClick={() => void loadOverview(false, false)} aria-label="刷新监控" />
            </Tooltip>
          </Space>
        }
      >
        <Row gutter={16}>
          <Col xs={24} md={12} lg={8}>
            <Card size="small">
              <Statistic title="CPU 占用" value={overview?.monitor.cpu_percent ?? 0} precision={1} suffix="%" />
              <Progress percent={Number((overview?.monitor.cpu_percent ?? 0).toFixed(1))} size="small" />
            </Card>
          </Col>
          <Col xs={24} md={12} lg={8}>
            <Card size="small">
              <Statistic title="内存占用" value={overview?.monitor.memory_percent ?? 0} precision={1} suffix="%" />
              <Progress percent={Number((overview?.monitor.memory_percent ?? 0).toFixed(1))} size="small" />
            </Card>
          </Col>
          <Col xs={24} md={12} lg={8}>
            <Card size="small">
              <Statistic title="磁盘占用" value={overview?.monitor.disk_percent ?? 0} precision={1} suffix="%" />
              <Progress percent={Number((overview?.monitor.disk_percent ?? 0).toFixed(1))} size="small" />
            </Card>
          </Col>
          <Col xs={24} md={12} lg={8}>
            <Card size="small">
              <Statistic title="转码任务数" value={overview?.monitor.transcode_task_count ?? 0} />
            </Card>
          </Col>
          <Col xs={24} md={12} lg={8}>
            <Card size="small">
              <Statistic title="媒体总数" value={overview?.monitor.media_total ?? 0} />
            </Card>
          </Col>
        </Row>
      </Card>

      <Card title="系统信息" loading={overviewLoading}>
        <Row gutter={16}>
          <Col xs={24} md={12} lg={8}><Statistic title="CPU 数量" value={overview?.system.cpu_count ?? 0} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title="内存大小" value={fmtBytesGB(overview?.system.memory_total)} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title="操作系统" value={overview?.system.os || "-"} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title="数据库" value={overview?.system.database || "-"} /></Col>
          <Col xs={24} md={12} lg={8}><Statistic title="软件版本" value={overview?.system.software_version || "dev"} /></Col>
        </Row>
      </Card>

      <Card title="当前活动" loading={overviewLoading}>
        <Table
          rowKey="id"
          pagination={{ pageSize: 10 }}
          dataSource={overview?.activities ?? []}
          columns={[
            { title: "时间", dataIndex: "created_at", width: 180 },
            { title: "用户", dataIndex: "username", width: 120, render: (v?: string) => v || "-" },
            { title: "动作", dataIndex: "action", width: 120 },
            { title: "媒体ID", dataIndex: "media_id", width: 100, render: (v: number) => (v > 0 ? v : "-") },
            { title: "说明", dataIndex: "message", ellipsis: true },
          ]}
        />
      </Card>
    </Space>
  );
}
