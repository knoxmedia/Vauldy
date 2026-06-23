import {
  Button,
  Input,
  message,
  Modal,
  Space,
  Spin,
  Statistic,
  Switch,
  Table,
  Tag,
  Typography,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import { ApiOutlined, SettingOutlined } from "@ant-design/icons";
import { useEffect, useState } from "react";
import type { AIProvider as AIProviderType } from "../api/client";
import { fetchAIProviders, saveAIProvider, testAIProvider } from "../api/client";
import { formatServerDateTime } from "../lib/datetime";
import { useT, type TranslateFn } from "../i18n";

function formatLastUsed(v: string | undefined, t: TranslateFn) {
  if (!v) return t("pages.ai_provider.never_used");
  return formatServerDateTime(v);
}

interface TableRow {
  key: string;
  id: string;
  name: string;
  enabled: boolean;
  api_url: string;
  api_key: string;
  model: string;
  request_count: number;
  token_count: number;
  last_used_at?: string;
}

type ProviderTestState =
  | { status: "loading" }
  | { status: "done"; ok: boolean; message: string };

export default function AIProviderPage() {
  const t = useT();
  const [rows, setRows] = useState<TableRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState<TableRow | null>(null);
  const [editUrl, setEditUrl] = useState("");
  const [editKey, setEditKey] = useState("");
  const [editModel, setEditModel] = useState("");
  const [editEnabled, setEditEnabled] = useState(false);
  const [saving, setSaving] = useState(false);
  const [testResults, setTestResults] = useState<Record<string, ProviderTestState>>({});
  const [testingId, setTestingId] = useState<string | null>(null);

  const load = () => {
    setLoading(true);
    fetchAIProviders()
      .then((items) => {
        setRows(
          items.map((p: AIProviderType) => ({
            key: p.id,
            id: p.id,
            name: p.name,
            enabled: p.enabled === 1,
            api_url: p.api_url,
            api_key: p.api_key,
            model: p.model,
            request_count: p.request_count,
            token_count: p.token_count,
            last_used_at: p.last_used_at,
          })),
        );
      })
      .catch(() => message.error(t("pages.ai_provider.load_failed")))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    load();
     
  }, []);

  const openEdit = (r: TableRow) => {
    setEditing(r);
    setEditUrl(r.api_url);
    setEditKey("");
    setEditModel(r.model);
    setEditEnabled(r.enabled);
  };

  const runProviderTest = async (id: string) => {
    setTestingId(id);
    setTestResults((prev) => ({ ...prev, [id]: { status: "loading" } }));
    try {
      const result = await testAIProvider(id);
      setTestResults((prev) => ({
        ...prev,
        [id]: { status: "done", ok: result.ok, message: result.message },
      }));
    } catch {
      setTestResults((prev) => ({
        ...prev,
        [id]: { status: "done", ok: false, message: t("pages.ai_provider.test_request_failed") },
      }));
    } finally {
      setTestingId(null);
    }
  };

  const handleSave = async () => {
    if (!editing) return;
    setSaving(true);
    try {
      await saveAIProvider(editing.id, {
        api_url: editUrl,
        api_key: editKey,
        model: editModel,
        enabled: editEnabled ? 1 : 0,
      });
      message.success(t("pages.ai_provider.saved_template", { name: editing.name }));
      setEditing(null);
      load();
    } catch {
      message.error(t("pages.ai_provider.save_failed"));
    } finally {
      setSaving(false);
    }
  };

  const columns: ColumnsType<TableRow> = [
    {
      title: t("pages.ai_provider.col_provider"),
      dataIndex: "name",
      width: 180,
      render: (name: string, r) => {
        const test = testResults[r.id];
        return (
          <div>
            <div>{name}</div>
            {test?.status === "loading" ? (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                <Spin size="small" style={{ marginRight: 6 }} />
                {t("pages.ai_provider.testing")}
              </Typography.Text>
            ) : test?.status === "done" ? (
              <Typography.Text
                type={test.ok ? "success" : "danger"}
                style={{ fontSize: 12, display: "block", marginTop: 4 }}
              >
                {test.message}
              </Typography.Text>
            ) : null}
          </div>
        );
      },
    },
    {
      title: t("pages.ai_provider.col_status"),
      key: "status",
      width: 100,
      render: (_, r) =>
        r.enabled ? <Tag color="green">{t("pages.ai_provider.status_enabled")}</Tag> : <Tag>{t("pages.ai_provider.status_disabled")}</Tag>,
    },
    {
      title: t("pages.ai_provider.col_api_url"),
      dataIndex: "api_url",
      width: 240,
      ellipsis: true,
    },
    {
      title: t("pages.ai_provider.col_api_key"),
      key: "keyStatus",
      width: 110,
      render: (_, r) =>
        r.api_key ? <Tag color="blue">{t("pages.ai_provider.key_set")}</Tag> : <Tag>{t("pages.ai_provider.key_unset")}</Tag>,
    },
    {
      title: t("pages.ai_provider.col_model"),
      dataIndex: "model",
      width: 160,
      render: (v) => v || "-",
    },
    {
      title: t("pages.ai_provider.col_request_count"),
      dataIndex: "request_count",
      width: 110,
      align: "right",
    },
    {
      title: t("pages.ai_provider.col_token_count"),
      dataIndex: "token_count",
      width: 110,
      align: "right",
    },
    {
      title: t("pages.ai_provider.col_last_used"),
      dataIndex: "last_used_at",
      width: 180,
      render: (v) => formatLastUsed(v, t),
    },
    {
      title: t("pages.ai_provider.col_actions"),
      key: "actions",
      width: 180,
      fixed: "right",
      align: "center",
      onCell: () => ({ style: { whiteSpace: "nowrap" } }),
      render: (_, r) => (
        <Space size={4}>
          <Button
            size="small"
            icon={<SettingOutlined />}
            onClick={() => openEdit(r)}
          >
            {t("pages.ai_provider.btn_settings")}
          </Button>
          <Button
            size="small"
            icon={<ApiOutlined />}
            loading={testingId === r.id}
            onClick={() => runProviderTest(r.id)}
          >
            {t("pages.ai_provider.btn_test")}
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Table
        rowKey="key"
        loading={loading}
        dataSource={rows}
        pagination={false}
        columns={columns}
        scroll={{ x: 1370 }}
      />

      <Modal
        title={editing ? t("pages.ai_provider.modal_title", { name: editing.name }) : ""}
        open={editing !== null}
        onOk={handleSave}
        onCancel={() => setEditing(null)}
        okText={t("pages.ai_provider.modal_save")}
        cancelText={t("pages.ai_provider.modal_cancel")}
        confirmLoading={saving}
      >
        <div style={{ display: "flex", flexDirection: "column", gap: 16, marginTop: 8 }}>
          <div>
            <div style={{ marginBottom: 4 }}>{t("pages.ai_provider.field_api_url")}</div>
            <Input
              placeholder={t("pages.ai_provider.field_api_url_placeholder")}
              value={editUrl}
              onChange={(e) => setEditUrl(e.target.value)}
            />
          </div>
          <div>
            <div style={{ marginBottom: 4 }}>{t("pages.ai_provider.field_api_key")}</div>
            <Input.Password
              placeholder={editing?.api_key ? t("pages.ai_provider.field_api_key_placeholder_keep") : t("pages.ai_provider.field_api_key_placeholder_enter")}
              value={editKey}
              onChange={(e) => setEditKey(e.target.value)}
            />
          </div>
          <div>
            <div style={{ marginBottom: 4 }}>{t("pages.ai_provider.field_model")}</div>
            <Input
              placeholder={t("pages.ai_provider.field_model_placeholder")}
              value={editModel}
              onChange={(e) => setEditModel(e.target.value)}
            />
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <span>{t("pages.ai_provider.field_enable")}</span>
            <Switch checked={editEnabled} onChange={setEditEnabled} />
          </div>

          {editing && (
            <Space size="large" style={{ paddingTop: 8, borderTop: "1px solid #303030" }}>
              <Statistic title={t("pages.ai_provider.stat_request_count")} value={editing.request_count} />
              <Statistic title={t("pages.ai_provider.stat_token_count")} value={editing.token_count} />
              <Statistic
                title={t("pages.ai_provider.stat_last_used")}
                value={formatLastUsed(editing.last_used_at, t)}
              />
            </Space>
          )}
        </div>
      </Modal>
    </div>
  );
}
