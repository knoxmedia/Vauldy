import { useEffect, useState } from "react";
import {
  Button,
  Card,
  Drawer,
  Form,
  Input,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  message,
} from "antd";
import { PlusOutlined, EditOutlined, DeleteOutlined, ExperimentOutlined } from "@ant-design/icons";
import { fetchWebhooks, createWebhook, updateWebhook, deleteWebhook, testWebhook, type Webhook } from "../../api/pretranscode";
import { useT } from "../../i18n";

const EVENT_OPTIONS = [
  { value: "task.completed", label: "task.completed" },
  { value: "task.failed", label: "task.failed" },
  { value: "rendition.completed", label: "rendition.completed" },
  { value: "rendition.failed", label: "rendition.failed" },
];

export function WebhookTab() {
  const t = useT();
  const [webhooks, setWebhooks] = useState<Webhook[]>([]);
  const [loading, setLoading] = useState(true);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<Webhook | null>(null);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try {
      setWebhooks(await fetchWebhooks());
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, []);

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({ events: ["task.completed"], is_enabled: true });
    setDrawerOpen(true);
  };

  const openEdit = (w: Webhook) => {
    setEditing(w);
    form.setFieldsValue(w);
    setDrawerOpen(true);
  };

  const submit = async () => {
    const values = (await form.validateFields()) as Webhook;
    try {
      if (editing && editing.id) {
        await updateWebhook(editing.id, values);
      } else {
        await createWebhook(values);
      }
      message.success(t("common.save") + " ✓");
      setDrawerOpen(false);
      load();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const onDelete = async (w: Webhook) => {
    if (!w.id) return;
    try {
      await deleteWebhook(w.id);
      load();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const onTest = async (w: Webhook) => {
    if (!w.id) return;
    try {
      await testWebhook(w.id);
      message.success("Test event sent");
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const columns = [
    { title: t("pretranscode.webhook.name"), dataIndex: "name" },
    { title: "URL", dataIndex: "url", ellipsis: true },
    {
      title: t("pretranscode.webhook.events"),
      dataIndex: "events",
      render: (events: string[]) => (events ?? []).map((e) => <Tag key={e}>{e}</Tag>),
    },
    {
      title: t("pretranscode.webhook.enabled"),
      dataIndex: "is_enabled",
      width: 80,
      render: (v: boolean) => (v ? <Tag color="green">ON</Tag> : <Tag>OFF</Tag>),
    },
    {
      title: "",
      key: "actions",
      width: 160,
      render: (_: unknown, w: Webhook) => (
        <Space size="small">
          <Button size="small" icon={<ExperimentOutlined />} onClick={() => onTest(w)} />
          <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(w)} />
          <Button size="small" danger icon={<DeleteOutlined />} onClick={() => onDelete(w)} />
        </Space>
      ),
    },
  ];

  return (
    <Card
      title={t("pretranscode.webhook.title")}
      extra={
        <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
          {t("pretranscode.webhook.create")}
        </Button>
      }
    >
      <Table rowKey="id" loading={loading} dataSource={webhooks} columns={columns} pagination={{ pageSize: 10 }} />
      <Drawer
        title={editing ? t("pretranscode.webhook.edit") : t("pretranscode.webhook.create")}
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        width={520}
        extra={
          <Space>
            <Button onClick={() => setDrawerOpen(false)}>{t("common.cancel")}</Button>
            <Button type="primary" onClick={submit}>
              {t("common.save")}
            </Button>
          </Space>
        }
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label={t("pretranscode.webhook.name")} rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="url" label="URL" rules={[{ required: true }]}>
            <Input placeholder="https://example.com/webhook" />
          </Form.Item>
          <Form.Item name="events" label={t("pretranscode.webhook.events")} rules={[{ required: true }]}>
            <Select mode="multiple" options={EVENT_OPTIONS} />
          </Form.Item>
          <Form.Item name="secret" label={t("pretranscode.webhook.secret")}>
            <Input.Password placeholder="HMAC-SHA256 secret" />
          </Form.Item>
          <Form.Item name="headers" label={t("pretranscode.webhook.headers")}>
            <Input.TextArea rows={3} placeholder={`{"Authorization": "Bearer xyz"}`} />
          </Form.Item>
          <Form.Item name="is_enabled" label={t("pretranscode.webhook.enabled")} valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Drawer>
    </Card>
  );
}
