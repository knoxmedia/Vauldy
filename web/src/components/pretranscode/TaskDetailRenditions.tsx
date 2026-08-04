import { useEffect, useState } from "react";
import { Alert, Button, Progress, Space, Table, Tag, message } from "antd";
import { StopOutlined, ReloadOutlined } from "@ant-design/icons";
import {
  fetchRenditionJobs,
  cancelRenditionJob,
  retryRenditionJob,
  type RenditionJob,
} from "../../api/pretranscode";
import { useT } from "../../i18n";

const STATUS_COLOR: Record<string, string> = {
  waiting: "default",
  running: "processing",
  done: "success",
  failed: "error",
  cancelled: "warning",
};

export function TaskDetailRenditions({ taskId }: { taskId: number }) {
  const t = useT();
  const [jobs, setJobs] = useState<RenditionJob[]>([]);
  const [loading, setLoading] = useState(true);

  const load = async () => {
    setLoading(true);
    try {
      setJobs(await fetchRenditionJobs(taskId));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
    const timer = setInterval(load, 3000);
    return () => clearInterval(timer);
  }, [taskId]);

  const onCancel = async (j: RenditionJob) => {
    try {
      await cancelRenditionJob(taskId, j.id);
      load();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const onRetry = async (j: RenditionJob) => {
    try {
      await retryRenditionJob(taskId, j.id);
      load();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const columns = [
    {
      title: t("pretranscode.preset.rendition_name"),
      dataIndex: "rendition_name",
      width: 100,
    },
    {
      title: t("pretranscode.task.rendition_status"),
      dataIndex: "status",
      width: 110,
      render: (s: string) => <Tag color={STATUS_COLOR[s] || "default"}>{t(`pretranscode.task.rendition_${s}`)}</Tag>,
    },
    {
      title: t("pretranscode.task.progress_overall"),
      dataIndex: "progress",
      render: (p: number, r: RenditionJob) => (
        <Progress percent={r.status === "done" ? 100 : p} size="small" status={r.status === "failed" ? "exception" : undefined} />
      ),
    },
    {
      title: t("pretranscode.preset.video_codec"),
      dataIndex: "encoder_used",
      width: 120,
      render: (v: string) => v || "-",
    },
    {
      title: "",
      key: "actions",
      width: 120,
      render: (_: unknown, r: RenditionJob) => (
        <Space size="small">
          {(r.status === "waiting" || r.status === "running") && (
            <Button size="small" icon={<StopOutlined />} onClick={() => onCancel(r)}>
              {t("pretranscode.task.cancel")}
            </Button>
          )}
          {(r.status === "failed" || r.status === "cancelled") && (
            <Button size="small" icon={<ReloadOutlined />} onClick={() => onRetry(r)}>
              {t("pretranscode.task.retry")}
            </Button>
          )}
        </Space>
      ),
    },
  ];

  if (jobs.length === 0 && !loading) {
    return <Alert type="info" showIcon message={t("pretranscode.task.no_renditions")} />;
  }

  return <Table rowKey="id" loading={loading} dataSource={jobs} columns={columns} pagination={false} size="small" />;
}
