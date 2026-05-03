import { Alert, Button, Card, Form, Input, Modal, Space, Table, Tag, Typography, message } from "antd";
import { useEffect, useState } from "react";
import {
  createApiClient,
  listApiClients,
  revokeApiClient,
  type APIClientRow,
  type CreateApiClientResult,
} from "../api/client";

export default function ApiCredentialsPage() {
  const [rows, setRows] = useState<APIClientRow[]>([]);
  const [loading, setLoading] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [secretOpen, setSecretOpen] = useState(false);
  const [created, setCreated] = useState<CreateApiClientResult | null>(null);
  const [form] = Form.useForm();

  async function load() {
    setLoading(true);
    try {
      setRows(await listApiClients());
    } catch (e: unknown) {
      message.error((e as Error).message || "加载失败");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  const tokenEndpoint = `${window.location.origin}/api/v1/oauth/token`;

  return (
    <Card title="API 凭证管理">
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Alert
          type="info"
          showIcon
          message="第三方应用通过 OAuth2 Client Credentials 获取访问令牌"
          description={
            <div>
              <Typography.Paragraph style={{ marginBottom: 8 }}>
                向{" "}
                <Typography.Text code copyable>
                  {tokenEndpoint}
                </Typography.Text>{" "}
                发送 <Typography.Text code>POST</Typography.Text>，参数{" "}
                <Typography.Text code>grant_type=client_credentials</Typography.Text>、
                <Typography.Text code>client_id</Typography.Text>、<Typography.Text code>client_secret</Typography.Text>
                （支持 <Typography.Text code>application/x-www-form-urlencoded</Typography.Text> 或 JSON）。
              </Typography.Paragraph>
              <Typography.Paragraph copyable code style={{ marginBottom: 0, whiteSpace: "pre-wrap", fontSize: 12 }}>
                {`curl -s -X POST "${tokenEndpoint}" \\
  -H "Content-Type: application/x-www-form-urlencoded" \\
  -d "grant_type=client_credentials&client_id=YOUR_CLIENT_ID&client_secret=YOUR_CLIENT_SECRET"`}
              </Typography.Paragraph>
            </div>
          }
        />

        <div>
          <Button type="primary" onClick={() => setCreateOpen(true)}>
            创建应用
          </Button>
        </div>

        <Table<APIClientRow>
          rowKey="app_id"
          loading={loading}
          dataSource={rows}
          pagination={false}
          columns={[
            { title: "AppID", dataIndex: "app_id", width: 90 },
            { title: "名称", dataIndex: "name" },
            { title: "描述", dataIndex: "description", ellipsis: true },
            {
              title: "client_id",
              dataIndex: "client_id",
              render: (v: string) => (
                <Typography.Text copyable code style={{ fontSize: 12 }}>
                  {v}
                </Typography.Text>
              ),
            },
            {
              title: "状态",
              width: 100,
              render: (_, r) => (r.revoked ? <Tag>已吊销</Tag> : <Tag color="green">有效</Tag>),
            },
            { title: "创建时间", dataIndex: "created_at", width: 180 },
            {
              title: "操作",
              width: 120,
              render: (_, r) =>
                r.revoked ? (
                  "—"
                ) : (
                  <Button
                    size="small"
                    danger
                    onClick={() => {
                      Modal.confirm({
                        title: "吊销该应用？",
                        content: "吊销后其 client_secret 将无法再换取令牌。",
                        onOk: async () => {
                          await revokeApiClient(r.app_id);
                          message.success("已吊销");
                          await load();
                        },
                      });
                    }}
                  >
                    吊销
                  </Button>
                ),
            },
          ]}
        />
      </Space>

      <Modal
        title="创建应用"
        open={createOpen}
        onCancel={() => {
          setCreateOpen(false);
          form.resetFields();
        }}
        onOk={async () => {
          const v = await form.validateFields();
          try {
            const res = await createApiClient({
              name: v.name,
              description: v.description?.trim() || "",
            });
            setCreated(res);
            setCreateOpen(false);
            form.resetFields();
            setSecretOpen(true);
            await load();
          } catch (e: unknown) {
            message.error((e as Error).message || "创建失败");
          }
        }}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: "请输入名称" }]}>
            <Input placeholder="例如：外部 CMS 同步" />
          </Form.Item>
          <Form.Item name="description" label="描述">
            <Input.TextArea rows={3} placeholder="用途说明（可选）" />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title="请保存凭证"
        open={secretOpen}
        onCancel={() => setSecretOpen(false)}
        footer={[
          <Button key="ok" type="primary" onClick={() => setSecretOpen(false)}>
            已保存
          </Button>,
        ]}
        width={560}
      >
        {created ? (
          <Space direction="vertical" style={{ width: "100%" }}>
            <Alert type="warning" showIcon message={created.hint || "client_secret 仅本次显示，关闭后无法再次查看"} />
            <div>
              <Typography.Text type="secondary">AppID</Typography.Text>
              <div>
                <Typography.Text code copyable>
                  {String(created.app_id)}
                </Typography.Text>
              </div>
            </div>
            <div>
              <Typography.Text type="secondary">client_id</Typography.Text>
              <div>
                <Typography.Text code copyable>
                  {created.client_id}
                </Typography.Text>
              </div>
            </div>
            <div>
              <Typography.Text type="secondary">client_secret</Typography.Text>
              <div>
                <Typography.Text code copyable style={{ wordBreak: "break-all" }}>
                  {created.client_secret}
                </Typography.Text>
              </div>
            </div>
          </Space>
        ) : null}
      </Modal>
    </Card>
  );
}
