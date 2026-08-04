import { useEffect, useState } from "react";
import { Card, Col, Descriptions, Row, Statistic, Table, Tag } from "antd";
import { fetchClusterNodes, type ClusterNode } from "../../api/pretranscode";
import { useT } from "../../i18n";

export function ClusterMonitor() {
  const t = useT();
  const [nodes, setNodes] = useState<ClusterNode[]>([]);
  const [queueDepth, setQueueDepth] = useState(0);
  const [activeTasks, setActiveTasks] = useState(0);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let mounted = true;
    const load = async () => {
      try {
        const data = await fetchClusterNodes();
        if (!mounted) return;
        setNodes(data.nodes ?? []);
        setQueueDepth(data.queue_depth ?? 0);
        setActiveTasks(data.total_active_tasks ?? 0);
      } finally {
        if (mounted) setLoading(false);
      }
    };
    load();
    const timer = setInterval(load, 5000);
    return () => {
      mounted = false;
      clearInterval(timer);
    };
  }, []);

  const columns = [
    { title: "ID", dataIndex: "id" },
    { title: "Host", dataIndex: "host" },
    {
      title: t("pretranscode.cluster.node_status"),
      dataIndex: "status",
      render: (s: string) => <Tag color={s === "online" ? "success" : "default"}>{t(`pretranscode.cluster.${s}`)}</Tag>,
    },
    {
      title: t("pretranscode.preset.video_codec"),
      dataIndex: "hardware_encoders",
      render: (encs: string[]) => (encs ?? []).join(", ") || "-",
    },
    {
      title: t("pretranscode.cluster.current_tasks"),
      dataIndex: "current_tasks",
    },
    {
      title: t("pretranscode.cluster.max_concurrent"),
      dataIndex: "max_concurrent",
    },
  ];

  return (
    <Card title={t("pretranscode.cluster.title")} loading={loading}>
      <Row gutter={16} style={{ marginBottom: 16 }}>
        <Col span={8}>
          <Statistic title={t("pretranscode.cluster.queue_depth")} value={queueDepth} />
        </Col>
        <Col span={8}>
          <Statistic title={t("pretranscode.cluster.current_tasks")} value={activeTasks} />
        </Col>
        <Col span={8}>
          <Statistic title={t("pretranscode.cluster.max_concurrent")} value={nodes[0]?.max_concurrent ?? 0} />
        </Col>
      </Row>
      <Table rowKey="id" dataSource={nodes} columns={columns} pagination={false} size="small" />
      <Descriptions size="small" style={{ marginTop: 16 }}>
        <Descriptions.Item label="Mode">standalone</Descriptions.Item>
      </Descriptions>
    </Card>
  );
}
