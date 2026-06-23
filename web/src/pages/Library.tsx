import { QuestionCircleOutlined } from "@ant-design/icons";
import { Button, Col, Divider, Drawer, Form, Grid, Input, Modal, Radio, Row, Select, Space, Switch, Table, Tag, Tooltip, message } from "antd";
import { useEffect, useState } from "react";
import LibraryProviderSourceTabs from "../components/LibraryProviderSourceTabs";
import {
  DEFAULT_IMAGE_PROVIDERS,
  DEFAULT_METADATA_PROVIDERS,
  normalizeProviderList,
  providerLabel,
} from "../lib/scrapeProviders";
import {
  cancelScanTask,
  Library,
  createLibrary,
  deleteLibrary,
  fetchLibrariesWithCapabilities,
  scanLibrary,
  updateLibrary,
} from "../api/client";
import { useT } from "../i18n";

export default function LibraryPage() {
  const t = useT();
  const [rows, setRows] = useState<Library[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Library | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [encryptedAssetsConfig, setEncryptedAssetsConfig] = useState<{ data_dot_encrypted_dir?: string }>({});
  const [form] = Form.useForm();
  const [providerSourceTab, setProviderSourceTab] = useState("metadata");
  const screens = Grid.useBreakpoint();

  function encDirRadioLabel(label: string, path: string) {
    return (
      <Space size={4}>
        {label}
        <Tooltip title={path}>
          <QuestionCircleOutlined
            style={{ color: "rgba(255, 255, 255, 0.45)", fontSize: 14 }}
            onClick={(e) => e.stopPropagation()}
          />
        </Tooltip>
      </Space>
    );
  }

  async function load(silent = false) {
    if (!silent) setLoading(true);
    try {
      const data = await fetchLibrariesWithCapabilities();
      setRows(data.items);
      setEncryptedAssetsConfig(data.encryptedAssetsConfig);
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.library.load_failed"));
    } finally {
      if (!silent) setLoading(false);
    }
  }

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => {
      void load(true);
    }, 3000);
    return () => window.clearInterval(timer);
     
  }, []);

  async function handleSubmit() {
    setSubmitting(true);
    try {
      const v = await form.validateFields();
      const folders = String(v.folders || "")
        .split(/\r?\n/)
        .map((x) => x.trim())
        .filter(Boolean);
      const metadataProviders = normalizeProviderList(v.metadata_providers);
      const imageProviders = normalizeProviderList(v.image_providers);
      const payload = {
        name: v.name,
        type: v.type,
        path: folders[0] || "",
        folders,
        scraper: metadataProviders[0] || "tmdb",
        auto_scan: v.auto_scan ? 1 : 0,
        enabled: v.enabled ? 1 : 0,
        realtime_monitor: v.realtime_monitor ? 1 : 0,
        preview_extract: v.preview_extract ? 1 : 0,
        drm_enabled: v.drm_enabled ? 1 : 0,
        encryption_mode: "standard" as const,
        encrypted_assets_enabled: v.encrypted_assets_enabled ? 1 : 0,
        encrypted_assets_cleanup_plaintext: v.encrypted_assets_cleanup_plaintext ? 1 : 0,
        encrypted_assets_dir_mode: v.encrypted_assets_enabled
          ? (v.encrypted_assets_dir_mode || "library")
          : "library",
        encrypted_assets_custom_dir:
          v.encrypted_assets_enabled && v.encrypted_assets_dir_mode === "custom"
            ? String(v.encrypted_assets_custom_dir || "").trim()
            : "",
        metadata_providers: metadataProviders,
        image_providers: imageProviders,
        metadata_refresh_policy: editing?.metadata_refresh_policy ?? "never",
      };
      if (editing) {
        await updateLibrary(editing.id, payload);
        message.success(t("pages.library.updated"));
      } else {
        await createLibrary(payload);
        message.success(t("pages.library.created"));
      }
      setOpen(false);
      setEditing(null);
      form.resetFields();
      await load();
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.library.save_failed"));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div>
      <Space style={{ marginBottom: 16 }}>
        <Button
          type="primary"
          onClick={() => {
            setEditing(null);
            setProviderSourceTab("metadata");
            form.resetFields();
            form.setFieldsValue({
              auto_scan: true,
              enabled: true,
              realtime_monitor: false,
              preview_extract: false,
              drm_enabled: false,
              encryption_mode: "standard",
              encrypted_assets_enabled: false,
              encrypted_assets_cleanup_plaintext: false,
              encrypted_assets_dir_mode: "library",
              encrypted_assets_custom_dir: "",
              metadata_providers: [...DEFAULT_METADATA_PROVIDERS],
              image_providers: [...DEFAULT_IMAGE_PROVIDERS],
            });
            setOpen(true);
          }}
        >
          {t("pages.library.create_btn")}
        </Button>
      </Space>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={rows}
        pagination={false}
        columns={[
          { title: t("pages.library.col_id"), dataIndex: "id", width: 70 },
          { title: t("pages.library.col_name"), dataIndex: "name" },
          { title: t("pages.library.col_type"), dataIndex: "type", width: 100 },
          {
            title: t("pages.library.col_folders"),
            dataIndex: "folders",
            render: (_: unknown, r) => (r.folders && r.folders.length > 0 ? r.folders.join(" | ") : r.path),
          },
          {
            title: t("pages.library.col_metadata_providers"),
            key: "metadata_providers",
            width: 140,
            render: (_: unknown, r) => {
              const providers = r.metadata_providers?.length ? r.metadata_providers : [r.scraper || "tmdb"];
              return providers.map((p) => providerLabel(p)).join(" > ");
            },
          },
          {
            title: t("pages.library.col_state"),
            key: "state",
            width: 300,
            render: (_, r) => (
              <Space size={4} wrap>
                <Tag color={r.enabled === 1 ? "green" : "default"}>{r.enabled === 1 ? t("pages.library.state_enabled") : t("pages.library.state_disabled")}</Tag>
                <Tag color={r.realtime_monitor === 1 ? "blue" : "default"}>{r.realtime_monitor === 1 ? t("pages.library.state_realtime") : t("pages.library.state_manual")}</Tag>
                {r.scan_status === "running" ? (
                  <Tag color="processing">
                    {t("pages.library.scanning", { processed: r.scan_processed_count ?? 0, added: r.scan_added_count ?? 0 })}
                  </Tag>
                ) : null}
              </Space>
            ),
          },
          {
            title: t("pages.library.col_actions"),
            key: "actions",
            width: 300,
            align: "center",
            render: (_, r) => (
              <Space>
                <Button
                  size="small"
                  onClick={() => {
                    setEditing(r);
                    setProviderSourceTab("metadata");
                    form.setFieldsValue({
                      name: r.name,
                      type: r.type,
                      folders: (r.folders && r.folders.length > 0 ? r.folders : [r.path]).join("\n"),
                      auto_scan: r.auto_scan === 1,
                      enabled: (r.enabled ?? 1) === 1,
                      realtime_monitor: (r.realtime_monitor ?? 0) === 1,
                      preview_extract: (r.preview_extract ?? 0) === 1,
                      drm_enabled: (r.drm_enabled ?? 0) === 1,
                      encryption_mode: "standard",
                      encrypted_assets_enabled: (r.encrypted_assets_enabled ?? 0) === 1,
                      encrypted_assets_cleanup_plaintext: (r.encrypted_assets_cleanup_plaintext ?? 0) === 1,
                      encrypted_assets_dir_mode: r.encrypted_assets_dir_mode || "library",
                      encrypted_assets_custom_dir: r.encrypted_assets_custom_dir || "",
                      metadata_providers: r.metadata_providers?.length
                        ? [...r.metadata_providers]
                        : [...DEFAULT_METADATA_PROVIDERS],
                      image_providers: r.image_providers?.length
                        ? [...r.image_providers]
                        : [...DEFAULT_IMAGE_PROVIDERS],
                    });
                    setOpen(true);
                  }}
                >
                  {t("pages.library.edit")}
                </Button>
                <Button
                  size="small"
                  onClick={async () => {
                    try {
                      const res = await scanLibrary(r.id);
                      if (res.running) {
                        message.warning(t("pages.library.scan_already_running", { task_id: res.task_id }));
                      } else {
                        message.success(t("pages.library.scan_started", { task_id: res.task_id }));
                      }
                      await load(true);
                    } catch (e: unknown) {
                      message.error((e as Error).message || t("pages.library.scan_failed"));
                    }
                  }}
                >
                  {t("pages.library.scan")}
                </Button>
                {r.scan_status === "running" && (r.scan_task_id ?? 0) > 0 ? (
                  <Button
                    size="small"
                    onClick={async () => {
                      try {
                        await cancelScanTask(r.scan_task_id!);
                        message.success(t("pages.library.cancel_scan_requested"));
                        await load(true);
                      } catch (e: unknown) {
                        message.error((e as Error).message || t("pages.library.cancel_failed"));
                      }
                    }}
                  >
                    {t("pages.library.cancel_scan")}
                  </Button>
                ) : null}
                <Button
                  size="small"
                  danger
                  onClick={() => {
                    Modal.confirm({
                      title: t("pages.library.delete_title"),
                      content: t("pages.library.delete_content"),
                      onOk: async () => {
                        await deleteLibrary(r.id);
                        message.success(t("pages.library.deleted"));
                        await load();
                      },
                    });
                  }}
                >
                  {t("pages.library.delete")}
                </Button>
              </Space>
            ),
          },
        ]}
      />

      <Drawer
        title={editing ? t("pages.library.drawer_edit_title") : t("pages.library.drawer_create_title")}
        open={open}
        width={screens.xl ? 880 : screens.lg ? 820 : screens.md ? 760 : screens.sm ? 620 : "92%"}
        onClose={() => {
          setOpen(false);
          setProviderSourceTab("metadata");
        }}
        footer={
          <Space>
            <Button onClick={() => setOpen(false)}>{t("pages.library.drawer_cancel")}</Button>
            <Button type="primary" loading={submitting} onClick={() => void handleSubmit()}>
              {t("pages.library.drawer_save")}
            </Button>
          </Space>
        }
      >
        <Form form={form} layout="vertical">
          <Divider style={{ marginTop: 0 }}>
            {t("pages.library.section_basic")}
          </Divider>
          <Row gutter={16}>
            <Col xs={24} md={12}>
              <Form.Item name="name" label={t("pages.library.field_name")} rules={[{ required: true }]}>
                <Input />
              </Form.Item>
            </Col>
            <Col xs={24} md={12}>
              <Form.Item name="type" label={t("pages.library.field_type")} rules={[{ required: true }]} initialValue="movie">
                <Select
                  options={[
                    { value: "movie", label: t("pages.library.type_movie") },
                    { value: "tv", label: t("pages.library.type_tv") },
                    { value: "anime", label: t("pages.library.type_anime") },
                    { value: "video", label: t("pages.library.type_video") },
                    { value: "music", label: t("pages.library.type_music") },
                    { value: "photo", label: t("pages.library.type_photo") },
                    { value: "document", label: t("pages.library.type_document") },
                  ]}
                />
              </Form.Item>
            </Col>
            <Col xs={24}>
              <Form.Item name="folders" label={t("pages.library.field_folders")} rules={[{ required: true }]}>
                <Input.TextArea rows={4} placeholder={t("pages.library.field_folders_placeholder")} />
              </Form.Item>
            </Col>
          </Row>

          <Divider>{t("pages.library.section_switches")}</Divider>
          <Row gutter={16}>
            <Col xs={24} sm={12} md={8}>
              <Form.Item name="enabled" label={t("pages.library.field_enabled")} valuePropName="checked" initialValue>
                <Switch />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12} md={8}>
              <Form.Item name="auto_scan" label={t("pages.library.field_auto_scan")} valuePropName="checked" initialValue>
                <Switch />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12} md={8}>
              <Form.Item
                name="realtime_monitor"
                label={t("pages.library.field_realtime_monitor")}
                valuePropName="checked"
                initialValue={false}
              >
                <Switch />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12} md={8}>
              <Form.Item
                name="preview_extract"
                label={t("pages.library.field_preview_extract")}
                valuePropName="checked"
                initialValue={false}
              >
                <Switch />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12} md={8}>
              <Form.Item
                name="encrypted_assets_enabled"
                label={t("pages.library.field_encrypted_assets")}
                tooltip={t("pages.library.field_encrypted_assets_hint")}
                valuePropName="checked"
                initialValue={false}
              >
                <Switch />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12} md={8}>
              <Form.Item name="drm_enabled" label={t("pages.library.field_drm_enabled")} valuePropName="checked" initialValue={false}>
                <Switch
                  onChange={(checked) => {
                    if (checked) form.setFieldValue("encryption_mode", "standard");
                  }}
                />
              </Form.Item>
            </Col>
          </Row>

          <Form.Item name="encryption_mode" hidden initialValue="standard">
            <Input />
          </Form.Item>

          <Form.Item
            noStyle
            shouldUpdate={(prev, next) =>
              prev.encrypted_assets_enabled !== next.encrypted_assets_enabled ||
              prev.folders !== next.folders ||
              prev.encrypted_assets_dir_mode !== next.encrypted_assets_dir_mode
            }
          >
            {({ getFieldValue }) => {
              if (!getFieldValue("encrypted_assets_enabled")) {
                return null;
              }
              const folderRoot =
                String(getFieldValue("folders") || "")
                  .split(/\r?\n/)[0]
                  ?.trim() || t("pages.library.encrypted_assets_dir_library_placeholder");
              const libraryEncPath = `${folderRoot}/.encrypted/`;
              const dataEncPath = `${encryptedAssetsConfig.data_dot_encrypted_dir || "…"}/`;
              return (
                <Row gutter={16}>
                  <Col xs={24}>
                    <Form.Item
                      name="encrypted_assets_dir_mode"
                      label={t("pages.library.field_encrypted_assets_dir_mode")}
                      initialValue="library"
                    >
                      <Radio.Group>
                        <Radio value="library">
                          {encDirRadioLabel(t("pages.library.encrypted_assets_dir_library"), libraryEncPath)}
                        </Radio>
                        <Radio value="data">
                          {encDirRadioLabel(t("pages.library.encrypted_assets_dir_data"), dataEncPath)}
                        </Radio>
                        <Radio value="custom">{t("pages.library.encrypted_assets_dir_custom")}</Radio>
                      </Radio.Group>
                    </Form.Item>
                  </Col>
                  <Form.Item noStyle shouldUpdate={(prev, next) => prev.encrypted_assets_dir_mode !== next.encrypted_assets_dir_mode}>
                    {({ getFieldValue: getDirMode }) =>
                      getDirMode("encrypted_assets_dir_mode") === "custom" ? (
                        <Col xs={24}>
                          <Form.Item
                            name="encrypted_assets_custom_dir"
                            label={t("pages.library.field_encrypted_assets_custom_dir")}
                            rules={[
                              {
                                validator: async (_, value) => {
                                  if (getDirMode("encrypted_assets_dir_mode") !== "custom") return;
                                  if (!String(value || "").trim()) {
                                    throw new Error(t("pages.library.encrypted_assets_custom_dir_required"));
                                  }
                                },
                              },
                            ]}
                          >
                            <Input placeholder={t("pages.library.field_encrypted_assets_custom_dir_placeholder")} />
                          </Form.Item>
                        </Col>
                      ) : null
                    }
                  </Form.Item>
                  <Col xs={24} sm={12} md={8}>
                    <Form.Item
                      name="encrypted_assets_cleanup_plaintext"
                      label={t("pages.library.field_encrypted_assets_cleanup")}
                      valuePropName="checked"
                      initialValue={false}
                    >
                      <Switch />
                    </Form.Item>
                  </Col>
                </Row>
              );
            }}
          </Form.Item>

          <Divider>{t("pages.library.section_metadata_policy")}</Divider>
          <Row gutter={16}>
            <Col xs={24}>
              <LibraryProviderSourceTabs activeKey={providerSourceTab} onChange={setProviderSourceTab} />
            </Col>
          </Row>
        </Form>
      </Drawer>
    </div>
  );
}
