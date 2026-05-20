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

function formatLastUsed(v?: string) {
  if (!v) return "从未使用";
  return new Date(v).toLocaleString();
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
      .catch(() => message.error("加载 AI 提供商失败"))
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
        [id]: { status: "done", ok: false, message: "测试请求失败" },
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
      message.success(`${editing.name} 已保存`);
      setEditing(null);
      load();
    } catch {
      message.error("保存失败");
    } finally {
      setSaving(false);
    }
  };

  const columns: ColumnsType<TableRow> = [
    {
      title: "提供商",
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
                测试中…
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
      title: "状态",
      key: "status",
      width: 100,
      render: (_, r) =>
        r.enabled ? <Tag color="green">已启用</Tag> : <Tag>已停用</Tag>,
    },
    {
      title: "API 地址",
      dataIndex: "api_url",
      width: 240,
      ellipsis: true,
    },
    {
      title: "API Key",
      key: "keyStatus",
      width: 110,
      render: (_, r) =>
        r.api_key ? <Tag color="blue">已设置</Tag> : <Tag>未设置</Tag>,
    },
    {
      title: "模型",
      dataIndex: "model",
      width: 160,
      render: (v) => v || "-",
    },
    {
      title: "请求次数",
      dataIndex: "request_count",
      width: 110,
      align: "right",
    },
    {
      title: "Token 消耗",
      dataIndex: "token_count",
      width: 110,
      align: "right",
    },
    {
      title: "最近使用",
      dataIndex: "last_used_at",
      width: 180,
      render: (v) => formatLastUsed(v),
    },
    {
      title: "操作",
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
            设置
          </Button>
          <Button
            size="small"
            icon={<ApiOutlined />}
            loading={testingId === r.id}
            onClick={() => runProviderTest(r.id)}
          >
            测试
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
        title={editing ? `设置 — ${editing.name}` : ""}
        open={editing !== null}
        onOk={handleSave}
        onCancel={() => setEditing(null)}
        okText="保存"
        cancelText="取消"
        confirmLoading={saving}
      >
        <div style={{ display: "flex", flexDirection: "column", gap: 16, marginTop: 8 }}>
          <div>
            <div style={{ marginBottom: 4 }}>API 地址</div>
            <Input
              placeholder="https://api.example.com/v1"
              value={editUrl}
              onChange={(e) => setEditUrl(e.target.value)}
            />
          </div>
          <div>
            <div style={{ marginBottom: 4 }}>API 密钥</div>
            <Input.Password
              placeholder={editing?.api_key ? "留空则不修改" : "输入 API Key"}
              value={editKey}
              onChange={(e) => setEditKey(e.target.value)}
            />
          </div>
          <div>
            <div style={{ marginBottom: 4 }}>模型</div>
            <Input
              placeholder="模型标识，如 gpt-4o"
              value={editModel}
              onChange={(e) => setEditModel(e.target.value)}
            />
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <span>启用该提供者</span>
            <Switch checked={editEnabled} onChange={setEditEnabled} />
          </div>

          {editing && (
            <Space size="large" style={{ paddingTop: 8, borderTop: "1px solid #303030" }}>
              <Statistic title="请求次数" value={editing.request_count} />
              <Statistic title="Token 消耗" value={editing.token_count} />
              <Statistic
                title="最近使用"
                value={formatLastUsed(editing.last_used_at)}
              />
            </Space>
          )}
        </div>
      </Modal>
    </div>
  );
}
