import { Alert, Button, Card, Form, Input, Modal, Space, Table, Tag, Typography, message } from "antd";
import { useEffect, useState } from "react";
import {
  createApiClient,
  listApiClients,
  revokeApiClient,
  type APIClientRow,
  type CreateApiClientResult,
} from "../api/client";
import { renderServerDateTime } from "../lib/datetime";
import { useT } from "../i18n";

export default function ApiCredentialsPage() {
  const t = useT();
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
      message.error((e as Error).message || t("pages.api_credentials.load_failed"));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
     
  }, []);

  const tokenEndpoint = `${window.location.origin}/api/v1/oauth/token`;

  return (
    <Card title={t("pages.api_credentials.title")}>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Alert
          type="info"
          showIcon
          message={t("pages.api_credentials.info_title")}
          description={
            <div>
              <Typography.Paragraph style={{ marginBottom: 8 }}>
                {t("pages.api_credentials.info_desc_prefix")}
                <Typography.Text code copyable>
                  {tokenEndpoint}
                </Typography.Text>
                {t("pages.api_credentials.info_desc_middle")}
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
            {t("pages.api_credentials.create_app")}
          </Button>
        </div>

        <Table<APIClientRow>
          rowKey="app_id"
          loading={loading}
          dataSource={rows}
          pagination={false}
          columns={[
            { title: t("pages.api_credentials.col_app_id"), dataIndex: "app_id", width: 90 },
            { title: t("pages.api_credentials.col_name"), dataIndex: "name" },
            { title: t("pages.api_credentials.col_description"), dataIndex: "description", ellipsis: true },
            {
              title: t("pages.api_credentials.col_client_id"),
              dataIndex: "client_id",
              render: (v: string) => (
                <Typography.Text copyable code style={{ fontSize: 12 }}>
                  {v}
                </Typography.Text>
              ),
            },
            {
              title: t("pages.api_credentials.col_status"),
              width: 100,
              render: (_, r) =>
                r.revoked ? (
                  <Tag>{t("pages.api_credentials.status_revoked")}</Tag>
                ) : (
                  <Tag color="green">{t("pages.api_credentials.status_active")}</Tag>
                ),
            },
            { title: t("pages.api_credentials.col_created_at"), dataIndex: "created_at", width: 180, render: renderServerDateTime },
            {
              title: t("pages.api_credentials.col_actions"),
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
                        title: t("pages.api_credentials.revoke_confirm_title"),
                        content: t("pages.api_credentials.revoke_confirm_content"),
                        onOk: async () => {
                          await revokeApiClient(r.app_id);
                          message.success(t("pages.api_credentials.revoke_success"));
                          await load();
                        },
                      });
                    }}
                  >
                    {t("pages.api_credentials.revoke_btn")}
                  </Button>
                ),
            },
          ]}
        />
      </Space>

      <Modal
        title={t("pages.api_credentials.create_title")}
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
            message.error((e as Error).message || t("pages.api_credentials.create_failed"));
          }
        }}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item
            name="name"
            label={t("pages.api_credentials.field_name")}
            rules={[{ required: true, message: t("pages.api_credentials.field_name_required") }]}
          >
            <Input placeholder={t("pages.api_credentials.field_name_placeholder")} />
          </Form.Item>
          <Form.Item name="description" label={t("pages.api_credentials.field_description")}>
            <Input.TextArea rows={3} placeholder={t("pages.api_credentials.field_description_placeholder")} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={t("pages.api_credentials.secret_modal_title")}
        open={secretOpen}
        onCancel={() => setSecretOpen(false)}
        footer={[
          <Button key="ok" type="primary" onClick={() => setSecretOpen(false)}>
            {t("pages.api_credentials.secret_modal_saved")}
          </Button>,
        ]}
        width={560}
      >
        {created ? (
          <Space direction="vertical" style={{ width: "100%" }}>
            <Alert type="warning" showIcon message={created.hint || t("pages.api_credentials.secret_warning_default")} />
            <div>
              <Typography.Text type="secondary">{t("pages.api_credentials.col_app_id")}</Typography.Text>
              <div>
                <Typography.Text code copyable>
                  {String(created.app_id)}
                </Typography.Text>
              </div>
            </div>
            <div>
              <Typography.Text type="secondary">{t("pages.api_credentials.col_client_id")}</Typography.Text>
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
