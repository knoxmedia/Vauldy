import { Button, Col, Divider, Drawer, Form, Grid, Input, Modal, Radio, Row, Select, Space, Switch, Table, Tag, message } from "antd";
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
  DRMCapabilities,
  Library,
  createLibrary,
  deleteLibrary,
  fetchLibrariesWithCapabilities,
  scanLibrary,
  updateLibrary,
} from "../api/client";

export default function LibraryPage() {
  const [rows, setRows] = useState<Library[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Library | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [drmCapabilities, setDrmCapabilities] = useState<DRMCapabilities>({
    widevine_enabled: true,
    powerdrm_enabled: true,
  });
  const [form] = Form.useForm();
  const [providerSourceTab, setProviderSourceTab] = useState("metadata");
  const screens = Grid.useBreakpoint();

  function defaultEncryptionMode(caps: DRMCapabilities): "standard" | "powerdrm" | "drm" {
    if (!caps.widevine_enabled && !caps.powerdrm_enabled) return "standard";
    if (caps.widevine_enabled) return "drm";
    if (caps.powerdrm_enabled) return "powerdrm";
    return "standard";
  }

  function normalizeEncryptionModeForCapabilities(
    mode: string | undefined,
    caps: DRMCapabilities
  ): "standard" | "powerdrm" | "drm" {
    if (mode === "powerdrm") return caps.powerdrm_enabled ? "powerdrm" : defaultEncryptionMode(caps);
    if (mode === "drm") return caps.widevine_enabled ? "drm" : defaultEncryptionMode(caps);
    return "standard";
  }

  async function load(silent = false) {
    if (!silent) setLoading(true);
    try {
      const data = await fetchLibrariesWithCapabilities();
      setRows(data.items);
      setDrmCapabilities(data.drmCapabilities);
    } catch (e: unknown) {
      message.error((e as Error).message || "加载失败");
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
        encryption_mode: v.drm_enabled
          ? normalizeEncryptionModeForCapabilities(v.encryption_mode, drmCapabilities)
          : "standard",
        cleanup_local_source_after_package: v.cleanup_local_source_after_package ? 1 : 0,
        metadata_providers: metadataProviders,
        image_providers: imageProviders,
        metadata_refresh_policy: editing?.metadata_refresh_policy ?? "never",
      };
      if (editing) {
        await updateLibrary(editing.id, payload);
        message.success("已更新");
      } else {
        await createLibrary(payload);
        message.success("已创建");
      }
      setOpen(false);
      setEditing(null);
      form.resetFields();
      await load();
    } catch (e: unknown) {
      message.error((e as Error).message || "保存失败");
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
              encryption_mode: defaultEncryptionMode(drmCapabilities),
              cleanup_local_source_after_package: false,
              metadata_providers: [...DEFAULT_METADATA_PROVIDERS],
              image_providers: [...DEFAULT_IMAGE_PROVIDERS],
            });
            setOpen(true);
          }}
        >
          新建媒体库
        </Button>
      </Space>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={rows}
        pagination={false}
        columns={[
          { title: "ID", dataIndex: "id", width: 70 },
          { title: "名称", dataIndex: "name" },
          { title: "类型", dataIndex: "type", width: 100 },
          {
            title: "文件夹",
            dataIndex: "folders",
            render: (_: unknown, r) => (r.folders && r.folders.length > 0 ? r.folders.join(" | ") : r.path),
          },
          {
            title: "元数据源",
            key: "metadata_providers",
            width: 140,
            render: (_: unknown, r) => {
              const providers = r.metadata_providers?.length ? r.metadata_providers : [r.scraper || "tmdb"];
              return providers.map((p) => providerLabel(p)).join(" > ");
            },
          },
          {
            title: "状态",
            key: "state",
            width: 300,
            render: (_, r) => (
              <Space size={4} wrap>
                <Tag color={r.enabled === 1 ? "green" : "default"}>{r.enabled === 1 ? "启用" : "停用"}</Tag>
                <Tag color={r.realtime_monitor === 1 ? "blue" : "default"}>{r.realtime_monitor === 1 ? "实时监控" : "手动同步"}</Tag>
                {r.scan_status === "running" ? (
                  <Tag color="processing">
                    扫描中 {r.scan_processed_count ?? 0} / 新增 {r.scan_added_count ?? 0}
                  </Tag>
                ) : null}
              </Space>
            ),
          },
          {
            title: "操作",
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
                      encryption_mode: normalizeEncryptionModeForCapabilities(
                        r.encryption_mode || "drm",
                        drmCapabilities
                      ),
                      cleanup_local_source_after_package: (r.cleanup_local_source_after_package ?? 0) === 1,
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
                  编辑
                </Button>
                <Button
                  size="small"
                  onClick={async () => {
                    try {
                      const res = await scanLibrary(r.id);
                      if (res.running) {
                        message.warning(`该媒体库已有扫描任务在运行（任务 #${res.task_id}）`);
                      } else {
                        message.success(`已启动扫描任务 #${res.task_id}`);
                      }
                      await load(true);
                    } catch (e: unknown) {
                      message.error((e as Error).message || "扫描失败");
                    }
                  }}
                >
                  扫描
                </Button>
                {r.scan_status === "running" && (r.scan_task_id ?? 0) > 0 ? (
                  <Button
                    size="small"
                    onClick={async () => {
                      try {
                        await cancelScanTask(r.scan_task_id!);
                        message.success("已请求取消扫描");
                        await load(true);
                      } catch (e: unknown) {
                        message.error((e as Error).message || "取消失败");
                      }
                    }}
                  >
                    取消扫描
                  </Button>
                ) : null}
                <Button
                  size="small"
                  danger
                  onClick={() => {
                    Modal.confirm({
                      title: "删除媒体库？",
                      content: "将删除库内索引的媒体记录。",
                      onOk: async () => {
                        await deleteLibrary(r.id);
                        message.success("已删除");
                        await load();
                      },
                    });
                  }}
                >
                  删除
                </Button>
              </Space>
            ),
          },
        ]}
      />

      <Drawer
        title={editing ? "编辑媒体库" : "新建媒体库"}
        open={open}
        width={screens.xl ? 880 : screens.lg ? 820 : screens.md ? 760 : screens.sm ? 620 : "92%"}
        onClose={() => {
          setOpen(false);
          setProviderSourceTab("metadata");
        }}
        footer={
          <Space>
            <Button onClick={() => setOpen(false)}>取消</Button>
            <Button type="primary" loading={submitting} onClick={() => void handleSubmit()}>
              保存
            </Button>
          </Space>
        }
      >
        <Form form={form} layout="vertical">
          <Divider style={{ marginTop: 0 }}>
            基础信息
          </Divider>
          <Row gutter={16}>
            <Col xs={24} md={12}>
              <Form.Item name="name" label="名称" rules={[{ required: true }]}>
                <Input />
              </Form.Item>
            </Col>
            <Col xs={24} md={12}>
              <Form.Item name="type" label="类型" rules={[{ required: true }]} initialValue="movie">
                <Select
                  options={[
                    { value: "movie", label: "电影" },
                    { value: "tv", label: "剧集" },
                    { value: "anime", label: "动漫" },
                    { value: "video", label: "其他影片" },
                    { value: "music", label: "音乐" },
                    { value: "photo", label: "图片" },
                    { value: "document", label: "文档" },
                  ]}
                />
              </Form.Item>
            </Col>
            <Col xs={24}>
              <Form.Item name="folders" label="文件夹（每行一个）" rules={[{ required: true }]}>
                <Input.TextArea rows={4} placeholder={"例如\nD:\\Videos\nE:\\Movies"} />
              </Form.Item>
            </Col>
          </Row>

          <Divider>功能开关</Divider>
          <Row gutter={16}>
            <Col xs={24} sm={12} md={8}>
              <Form.Item name="enabled" label="启用媒体库" valuePropName="checked" initialValue>
                <Switch />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12} md={8}>
              <Form.Item name="auto_scan" label="自动扫描" valuePropName="checked" initialValue>
                <Switch />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12} md={8}>
              <Form.Item
                name="realtime_monitor"
                label="实时监控（自动同步新增/修改/删除）"
                valuePropName="checked"
                initialValue={false}
              >
                <Switch />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12} md={8}>
              <Form.Item
                name="preview_extract"
                label="启用进度条预览图提取"
                valuePropName="checked"
                initialValue={false}
              >
                <Switch />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12} md={8}>
              <Form.Item name="drm_enabled" label="视频加密" valuePropName="checked" initialValue={false}>
                <Switch />
              </Form.Item>
            </Col>
            <Form.Item noStyle shouldUpdate={(prev, next) => prev.drm_enabled !== next.drm_enabled}>
              {({ getFieldValue }) =>
                getFieldValue("drm_enabled") ? (
                  <Col xs={24} sm={12} md={8}>
                    <Form.Item
                      name="encryption_mode"
                      label="加密方式"
                      initialValue={defaultEncryptionMode(drmCapabilities)}
                    >
                      <Radio.Group
                        options={[
                          { label: "标准加密", value: "standard" },
                          ...(drmCapabilities.powerdrm_enabled ? [{ label: "私有加密", value: "powerdrm" }] : []),
                          ...(drmCapabilities.widevine_enabled ? [{ label: "DRM加密", value: "drm" }] : []),
                        ]}
                      />
                    </Form.Item>
                  </Col>
                ) : null
              }
            </Form.Item>
            <Form.Item noStyle shouldUpdate={(prev, next) => prev.drm_enabled !== next.drm_enabled}>
              {({ getFieldValue }) =>
                getFieldValue("drm_enabled") ? (
                  <Col xs={24} sm={12} md={8}>
                    <Form.Item
                      name="cleanup_local_source_after_package"
                      label="视频加密后清理源视频"
                      valuePropName="checked"
                      initialValue={true}
                    >
                      <Switch />
                    </Form.Item>
                  </Col>
                ) : null
              }
            </Form.Item>
          </Row>

          <Divider>元数据策略</Divider>
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
