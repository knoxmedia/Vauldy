import { useCallback, useEffect, useState } from "react";
import { Card, Row, Col, Statistic, Table, Tag, Button, Tooltip, Alert, Space } from "antd";
import { ReloadOutlined, RightOutlined } from "@ant-design/icons";
import type { ColumnsType } from "antd/es/table";
import type { Overview, ProjectionRow } from "../../api/taskControl";
import { fetchTaskControlOverview } from "../../api/taskControl";
import { useT, tGlobal } from "../../i18n";

export interface TaskOverviewProps {
  onDrillDownType?: (taskType: string) => void;
  onSelectTask?: (taskId: string) => void;
}

function statusColor(status: string): string {
  switch (status) {
    case "waiting": return "#49aa19";
    case "running": return "#1677ff";
    case "done": return "#52c41a";
    case "failed": return "#ff4d4f";
    case "cancelled": return "#faad14";
    case "skipped": return "#888";
    default: return "#d9d9d9";
  }
}

function statusTag(status: string, text: string) {
  return <Tag color={statusColor(status)} style={{ margin: 0 }}>{text}</Tag>;
}

export function TaskOverview({ onDrillDownType, onSelectTask }: TaskOverviewProps) {
  const t = useT();
  const [data, setData] = useState<Overview | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const ov = await fetchTaskControlOverview();
      setData(ov);
    } catch {
      setError(tGlobal("tasks.control.load_failed"));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (error) {
    return (
      <Alert
        type="error"
        showIcon
        title={error}
        action={<Button size="small" onClick={load}>{t("tasks.control.retry")}</Button>}
      />
    );
  }

  if (!data) {
    return <div style={{ padding: 40, textAlign: "center", color: "#888" }}>{t("tasks.control.overview_loading")}</div>;
  }

  const sectionColumns: ColumnsType<ProjectionRow> = [
    {
      title: t("tasks.control.col_task_id"),
      dataIndex: "task_id",
      key: "task_id",
      width: 140,
      ellipsis: true,
      render: (v: string) => <span style={{ fontFamily: "monospace", fontSize: 12, color: "#1677ff" }}>{v}</span>,
    },
    {
      title: t("tasks.control.col_type"),
      dataIndex: "task_type",
      key: "task_type",
      width: 110,
      render: (v: string) => <Tag style={{ margin: 0 }}>{v}</Tag>,
    },
    {
      title: t("tasks.control.col_media"),
      key: "media",
      width: 220,
      ellipsis: true,
      render: (_: unknown, r: ProjectionRow) =>
        r.media_id ? (
          <span style={{ fontSize: 12 }}>
            <span style={{ color: "#888", fontFamily: "monospace" }}>#{r.media_id}</span>
            {r.media_title && <span style={{ marginLeft: 6, color: "#d9d9d9" }}>{r.media_title}</span>}
          </span>
        ) : (
          <span style={{ color: "#555" }}>-</span>
        ),
    },
    {
      title: t("tasks.control.col_status"),
      dataIndex: "normalized_status",
      key: "status",
      width: 100,
      render: (v: string) => statusTag(v, t(`tasks.control.status_${v}`)),
    },
    {
      title: t("tasks.control.col_info"),
      key: "info",
      ellipsis: true,
      render: (_: unknown, r: ProjectionRow) => (
        <span style={{ color: "#888", fontSize: 12 }}>
          {r.terminal_reason || `Gen ${r.generation} · Retry ${r.retry_round} · ${r.attempt}/${r.max_attempts}`}
        </span>
      ),
    },
    {
      title: t("tasks.control.col_updated"),
      dataIndex: "updated_at",
      key: "updated_at",
      width: 160,
      render: (v: string) => <span style={{ color: "#888", fontSize: 12 }}>{v ? new Date(v).toLocaleString() : "-"}</span>,
    },
  ];

  const sectionTitle = (label: string, color: string) => (
    <span style={{ color, fontSize: 14, fontWeight: 600 }}>{label}</span>
  );

  const sectionOnRow = (record: ProjectionRow) => ({
    onClick: () => onSelectTask?.(record.task_id),
    style: { cursor: "pointer" },
  });

  return (
    <Space orientation="vertical" size="large" style={{ width: "100%" }}>
      <Card
        title={t("tasks.control.status_summary")}
        loading={loading}
        extra={
          <Tooltip title={t("tasks.control.overview_refresh")}>
            <Button icon={<ReloadOutlined />} onClick={load} aria-label={t("tasks.control.overview_refresh")} />
          </Tooltip>
        }
        size="small"
      >
        <Row gutter={[12, 12]}>
          {[
            { k: "waiting", c: "#49aa19" },
            { k: "running", c: "#1677ff" },
            { k: "done", c: "#52c41a" },
            { k: "failed", c: "#ff4d4f" },
            { k: "cancelled", c: "#faad14" },
            { k: "skipped", c: "#888" },
          ].map(({ k, c }) => (
            <Col xs={12} sm={8} md={4} key={k}>
              <Card size="small">
                <Statistic
                  title={<span style={{ color: "#888", fontSize: 12 }}>{t(`tasks.control.status_${k}`)}</span>}
                  value={data.status_counts[k as keyof typeof data.status_counts] ?? 0}
                  styles={{ content: { color: c, fontSize: 24, fontWeight: 700 } }}
                />
              </Card>
            </Col>
          ))}
        </Row>
      </Card>

      <Card
        title={t("tasks.control.type_counts")}
        size="small"
      >
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          {Object.entries(data.type_counts)
            .sort(([, a], [, b]) => b - a)
            .map(([type, count]) => (
              <Button
                key={type}
                size="small"
                type={count > 0 ? "default" : "text"}
                disabled={count === 0}
                onClick={() => onDrillDownType?.(type)}
                style={{ color: count > 0 ? undefined : "#555" }}
              >
                {type}
                <span style={{ marginLeft: 6, fontWeight: 600, color: "#1677ff" }}>{count}</span>
                <RightOutlined style={{ marginLeft: 4, fontSize: 10 }} />
              </Button>
            ))}
        </div>
      </Card>

      {[
        { k: "running", label: t("tasks.control.section_running"), color: "#1677ff" },
        { k: "oldest", label: t("tasks.control.section_oldest"), color: "#49aa19" },
        { k: "blocked", label: t("tasks.control.section_blocked"), color: "#ff4d4f" },
        { k: "no_worker", label: t("tasks.control.section_no_worker"), color: "#faad14" },
        { k: "expired", label: t("tasks.control.section_expired"), color: "#faad14" },
        { k: "recovery", label: t("tasks.control.section_recovery"), color: "#1677ff" },
        { k: "cleanup", label: t("tasks.control.section_cleanup"), color: "#888" },
      ].map(({ k, label, color }) => {
        const section = data[k as keyof Overview] as { label: string; items: ProjectionRow[] } | undefined;
        if (!section || !section.items || section.items.length === 0) return null;
        return (
          <Card
            key={k}
            title={sectionTitle(label, color)}
            size="small"
          >
            <Table
              rowKey="task_id"
              dataSource={section.items}
              columns={sectionColumns}
              pagination={false}
              size="small"
              onRow={sectionOnRow}
              scroll={{ x: 600 }}
            />
          </Card>
        );
      })}
    </Space>
  );
}
