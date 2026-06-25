import {
  Alert,
  Avatar,
  Button,
  Card,
  Col,
  Collapse,
  Descriptions,
  Divider,
  Form,
  Image,
  Input,
  InputNumber,
  List,
  Row,
  Select,
  Space,
  Tree,
  Typography,
  message,
} from "antd";
import type { DataNode } from "antd/es/tree";
import { useEffect, useMemo, useState } from "react";
import {
  fetchLibraries,
  fetchMedia,
  fetchMediaDetail,
  type Library,
  type MediaDetail,
  type MediaItem,
  updateMediaAdmin,
} from "../api/client";
import { useT } from "../i18n";

type EditorValues = {
  title?: string;
  original_title?: string;
  status?: string;
  duration?: number;
  width?: number;
  height?: number;
  bitrate?: number;
  format?: string;
  overview?: string;
  rating?: number;
  genres?: string;
  poster?: string;
  backdrop?: string;
  logo?: string;
  meta_json?: string;
};

type TreeNodeInfo = {
  type: "dir" | "file";
  key: string;
  name: string;
  path: string;
  mediaId?: number;
};

function safeParseMeta(raw?: string): Record<string, any> {
  const text = (raw || "").trim();
  if (!text) return {};
  try {
    const parsed = JSON.parse(text) as Record<string, any>;
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
}

function stringifyMeta(meta: Record<string, any>): string {
  return JSON.stringify(meta, null, 2);
}

function normalizePath(raw: string) {
  return (raw || "").replace(/\\/g, "/");
}

function toLibraryRelativePath(fullPath: string, libraryRoots?: string[]) {
  let full = normalizePath(fullPath).replace(/\/+$/, "");
  if (full.toLowerCase().startsWith("//?/unc/")) {
    full = "//" + full.slice("//?/unc/".length);
  } else if (full.toLowerCase().startsWith("//?/")) {
    full = full.slice(4);
  }
  const roots = (libraryRoots || [])
    .map((r) => normalizePath(r || "").replace(/\/+$/, ""))
    .filter(Boolean)
    .sort((a, b) => b.length - a.length);
  if (roots.length === 0) return full;
  const fullLower = full.toLowerCase();
  for (const root of roots) {
    const rootLower = root.toLowerCase();
    if (fullLower === rootLower) return "";
    if (fullLower.startsWith(`${rootLower}/`)) {
      return full.slice(root.length + 1);
    }
  }
  return full;
}

/** Strip Windows drive segments left over when root matching fails, so the tree lists folders under the library instead of k:/ f:/ roots. */
function stripLeadingWindowsDriveSegments(rel: string): string {
  const parts = normalizePath(rel)
    .replace(/^\/+/, "")
    .split("/")
    .filter(Boolean);
  while (parts.length > 0 && /^[a-zA-Z]:$/.test(parts[0])) {
    parts.shift();
  }
  return parts.join("/");
}

/** Relative path for tree, lists, and directory selection (never shows a leading drive letter as a fake root). */
function toLibraryDisplayRelativePath(fullPath: string, libraryRoots?: string[]) {
  return stripLeadingWindowsDriveSegments(toLibraryRelativePath(fullPath, libraryRoots));
}

function nodeTitle(name: string, kind: "dir" | "file") {
  return <span>{kind === "dir" ? `📁 ${name}` : `🎬 ${name}`}</span>;
}

export default function MediaManagerPage() {
  const t = useT();
  const [libs, setLibs] = useState<Library[]>([]);
  const [libraryId, setLibraryId] = useState<number | undefined>(undefined);
  const [rows, setRows] = useState<MediaItem[]>([]);
  const [selectedNode, setSelectedNode] = useState<TreeNodeInfo | null>(null);
  const [loadingDetail, setLoadingDetail] = useState(false);
  const [saving, setSaving] = useState(false);
  const [detail, setDetail] = useState<MediaDetail | null>(null);
  const [treeKeyword, setTreeKeyword] = useState("");
  const [form] = Form.useForm<EditorValues>();
  const posterPreview = Form.useWatch("poster", form);
  const backdropPreview = Form.useWatch("backdrop", form);
  const logoPreview = Form.useWatch("logo", form);
  const selectedId = selectedNode?.type === "file" ? selectedNode.mediaId : undefined;
  const selectedLibrary = useMemo(
    () => libs.find((l) => l.id === libraryId),
    [libs, libraryId]
  );
  const selectedLibraryRoots = useMemo(() => {
    const roots = [...(selectedLibrary?.folders || []), selectedLibrary?.path || ""]
      .map((x) => (x || "").trim())
      .filter(Boolean);
    return Array.from(new Set(roots));
  }, [selectedLibrary?.folders, selectedLibrary?.path]);

  async function loadLibraries() {
    const items = await fetchLibraries();
    setLibs(items);
    if (items.length > 0) {
      setLibraryId((current) => (current !== undefined ? current : items[0].id));
    }
  }

  async function loadMedia(libId?: number) {
    const items = await fetchMedia(libId, { sort: "created_desc", limit: 500 });
    setRows(items);
    if (items.length === 0) {
      setSelectedNode(null);
      setDetail(null);
      form.resetFields();
    } else if (!selectedId || !items.some((x) => x.id === selectedId)) {
      const first = items[0];
      setSelectedNode({
        type: "file",
        key: `file:${first.id}`,
        name: first.title || first.file_id,
        path: toLibraryDisplayRelativePath(first.file_path || "", selectedLibraryRoots),
        mediaId: first.id,
      });
    }
  }

  async function loadDetail(id: number) {
    setLoadingDetail(true);
    try {
      const d = await fetchMediaDetail(id);
      setDetail(d);
      form.setFieldsValue({
        title: d.title || "",
        original_title: d.original_title || "",
        status: d.status || "active",
        duration: d.duration || 0,
        width: d.width || 0,
        height: d.height || 0,
        bitrate: d.bitrate || 0,
        format: d.format || "",
        meta_json: stringifyMeta(safeParseMeta(d.meta_json)),
      });
      const parsed = safeParseMeta(d.meta_json);
      const scrape = (parsed.scrape || {}) as Record<string, any>;
      const extra = (scrape.extra || {}) as Record<string, any>;
      form.setFieldsValue({
        overview: typeof scrape.overview === "string" ? scrape.overview : "",
        rating: typeof scrape.rating === "number" ? scrape.rating : undefined,
        genres: Array.isArray(scrape.genres) ? scrape.genres.join(", ") : "",
        poster: typeof extra.poster === "string" ? extra.poster : "",
        backdrop: typeof extra.backdrop === "string" ? extra.backdrop : "",
        logo: typeof extra.logo === "string" ? extra.logo : "",
      });
    } finally {
      setLoadingDetail(false);
    }
  }

  useEffect(() => {
    void loadLibraries().catch((e: unknown) => message.error((e as Error).message || t("pages.media_manager.load_libraries_failed")));
     
  }, []);

  useEffect(() => {
    if (libraryId === undefined) return;
    void loadMedia(libraryId).catch((e: unknown) => message.error((e as Error).message || t("pages.media_manager.load_media_failed")));
     
  }, [libraryId]);

  useEffect(() => {
    if (!selectedId) return;
    void loadDetail(selectedId).catch((e: unknown) => message.error((e as Error).message || t("pages.media_manager.load_detail_failed")));
     
  }, [selectedId]);

  const { treeData, treeMap } = useMemo(() => {
    const root: DataNode[] = [];
    const map = new Map<string, TreeNodeInfo>();
    const getOrCreateDir = (segments: string[], fullPath: string): DataNode => {
      let cursor = root;
      let node: DataNode | undefined;
      let acc = "";
      segments.forEach((seg) => {
        acc = acc ? `${acc}/${seg}` : seg;
        let found = cursor.find((n) => n.key === `dir:${acc}`);
        if (!found) {
          found = {
            key: `dir:${acc}`,
            title: nodeTitle(seg, "dir"),
            children: [],
          };
          cursor.push(found);
          map.set(`dir:${acc}`, { type: "dir", key: `dir:${acc}`, name: seg, path: fullPath });
        }
        node = found;
        cursor = (found.children || []) as DataNode[];
      });
      return node!;
    };
    rows.forEach((m) => {
      const rel = toLibraryDisplayRelativePath(m.file_path || "", selectedLibraryRoots);
      const parts = rel.split("/").filter(Boolean);
      const fileName = parts.length > 0 ? parts[parts.length - 1] : String(m.id);
      const dirs = parts.slice(0, -1);
      let parentChildren = root;
      if (dirs.length > 0) {
        const dirNode = getOrCreateDir(dirs, dirs.join("/"));
        parentChildren = (dirNode.children || []) as DataNode[];
      }
      const fileKey = `file:${m.id}`;
      parentChildren.push({
        key: fileKey,
        title: nodeTitle(m.title || fileName, "file"),
        isLeaf: true,
      });
      map.set(fileKey, {
        type: "file",
        key: fileKey,
        mediaId: m.id,
        name: m.title || fileName,
        path: rel,
      });
    });
    return { treeData: root, treeMap: map };
  }, [rows, selectedLibraryRoots]);

  const filteredTreeData = useMemo(() => {
    const kw = treeKeyword.trim().toLowerCase();
    if (!kw) return treeData;
    const pass = (node: DataNode): DataNode | null => {
      const info = treeMap.get(String(node.key));
      const hit =
        !!info &&
        (info.name.toLowerCase().includes(kw) ||
          info.path.toLowerCase().includes(kw));
      const children = (node.children || [])
        .map((c) => pass(c as DataNode))
        .filter((c): c is DataNode => !!c);
      if (hit || children.length > 0) {
        return { ...node, children };
      }
      return null;
    };
    return treeData
      .map((n) => pass(n))
      .filter((n): n is DataNode => !!n);
  }, [treeData, treeMap, treeKeyword]);

  const dirFiles = useMemo(() => {
    if (selectedNode?.type !== "dir") return [];
    const prefix = selectedNode.path ? `${selectedNode.path}/` : "";
    return rows
      .filter((x) => {
        const p = toLibraryDisplayRelativePath(x.file_path || "", selectedLibraryRoots);
        return p.startsWith(prefix) && p !== selectedNode.path;
      })
      .sort((a, b) =>
        toLibraryDisplayRelativePath(a.file_path || "", selectedLibraryRoots).localeCompare(
          toLibraryDisplayRelativePath(b.file_path || "", selectedLibraryRoots)
        )
      );
  }, [rows, selectedNode, selectedLibraryRoots]);

  const onSave = async () => {
    if (!selectedId) return;
    const v = await form.validateFields();
    const parsed = safeParseMeta(v.meta_json);
    const scrape = (parsed.scrape && typeof parsed.scrape === "object" ? parsed.scrape : {}) as Record<string, any>;
    const extra = (scrape.extra && typeof scrape.extra === "object" ? scrape.extra : {}) as Record<string, any>;
    scrape.overview = (v.overview || "").trim();
    if (typeof v.rating === "number") {
      scrape.rating = v.rating;
    } else {
      delete scrape.rating;
    }
    const genres = (v.genres || "")
      .split(",")
      .map((x) => x.trim())
      .filter(Boolean);
    scrape.genres = genres;
    extra.poster = (v.poster || "").trim();
    extra.backdrop = (v.backdrop || "").trim();
    extra.logo = (v.logo || "").trim();
    scrape.extra = extra;
    parsed.scrape = scrape;
    const mergedMetaJSON = stringifyMeta(parsed);
    setSaving(true);
    try {
      await updateMediaAdmin(selectedId, {
        title: v.title ?? "",
        original_title: v.original_title ?? "",
        status: v.status ?? "active",
        duration: Number(v.duration ?? 0),
        width: Number(v.width ?? 0),
        height: Number(v.height ?? 0),
        bitrate: Number(v.bitrate ?? 0),
        format: v.format ?? "",
        meta_json: mergedMetaJSON,
      });
      message.success(t("pages.media_manager.saved"));
      await loadMedia(libraryId);
      await loadDetail(selectedId);
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.media_manager.save_failed"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t("pages.media_manager.intro")}
      </Typography.Paragraph>

      <Row gutter={16}>
        <Col xs={24} lg={11}>
          <Card
            title={t("pages.media_manager.tree_title")}
            extra={
              <Select
                style={{ width: 220 }}
                placeholder={t("pages.media_manager.library_placeholder")}
                value={libraryId}
                onChange={(v) => setLibraryId(v)}
                options={libs.map((l) => ({ value: l.id, label: l.name }))}
              />
            }
          >
            <Input
              allowClear
              placeholder={t("pages.media_manager.filter_placeholder")}
              value={treeKeyword}
              onChange={(e) => setTreeKeyword(e.target.value)}
              style={{ marginBottom: 10 }}
            />
            <Tree
              treeData={filteredTreeData}
              height={620}
              defaultExpandAll
              selectedKeys={selectedNode ? [selectedNode.key] : []}
              onSelect={(keys) => {
                const key = String(keys[0] || "");
                const node = treeMap.get(key);
                if (node) {
                  setSelectedNode(node);
                  if (node.type === "dir") {
                    setDetail(null);
                    form.resetFields();
                  }
                }
              }}
            />
          </Card>
        </Col>

        <Col xs={24} lg={13}>
          {selectedNode?.type === "dir" ? (
            <Card title={t("pages.media_manager.dir_info_prefix", { name: selectedNode.name })}>
              <Descriptions column={1} bordered size="small">
                <Descriptions.Item label={t("pages.media_manager.dir_name_label")}>{selectedNode.name}</Descriptions.Item>
                <Descriptions.Item label={t("pages.media_manager.dir_path_label")}>{selectedNode.path}</Descriptions.Item>
                <Descriptions.Item label={t("pages.media_manager.dir_file_count_label")}>
                  {rows.filter((x) => toLibraryDisplayRelativePath(x.file_path || "", selectedLibraryRoots).startsWith(selectedNode.path)).length}
                </Descriptions.Item>
              </Descriptions>
              <Collapse
                size="small"
                style={{ marginTop: 12 }}
                items={[
                  {
                    key: "debug-root",
                    label: t("pages.media_manager.debug_panel_title"),
                    children: (
                      <Descriptions column={1} bordered size="small">
                        <Descriptions.Item label={t("pages.media_manager.lib_root_path_label")}>
                          {selectedLibrary?.path || "-"}
                        </Descriptions.Item>
                      </Descriptions>
                    ),
                  },
                ]}
              />
              <Divider />
              <Space style={{ marginBottom: 8 }}>
                <Typography.Text strong>{t("pages.media_manager.dir_files_label")}</Typography.Text>
                <Button
                  size="small"
                  disabled={dirFiles.length === 0}
                  onClick={() => {
                    const first = dirFiles[0];
                    if (!first) return;
                    setSelectedNode({
                      type: "file",
                      key: `file:${first.id}`,
                      name: first.title || first.file_id,
                            path: toLibraryDisplayRelativePath(first.file_path || "", selectedLibraryRoots),
                      mediaId: first.id,
                    });
                  }}
                >
                  {t("pages.media_manager.edit_first")}
                </Button>
              </Space>
              <List
                size="small"
                bordered
                dataSource={dirFiles}
                style={{ maxHeight: 420, overflow: "auto" }}
                renderItem={(item, idx) => (
                  <List.Item
                    actions={[
                      <Button
                        key="edit"
                        size="small"
                        onClick={() =>
                          setSelectedNode({
                            type: "file",
                            key: `file:${item.id}`,
                            name: item.title || item.file_id,
                            path: toLibraryDisplayRelativePath(item.file_path || "", selectedLibraryRoots),
                            mediaId: item.id,
                          })
                        }
                      >
                        {t("pages.media_manager.btn_edit")}
                      </Button>,
                      <Button
                        key="next"
                        size="small"
                        disabled={idx >= dirFiles.length - 1}
                        onClick={() => {
                          const next = dirFiles[idx + 1];
                          if (!next) return;
                          setSelectedNode({
                            type: "file",
                            key: `file:${next.id}`,
                            name: next.title || next.file_id,
                            path: toLibraryDisplayRelativePath(next.file_path || "", selectedLibraryRoots),
                            mediaId: next.id,
                          });
                        }}
                      >
                        {t("pages.media_manager.btn_next")}
                      </Button>,
                    ]}
                  >
                    <List.Item.Meta
                      title={item.title || item.file_id}
                      description={toLibraryDisplayRelativePath(item.file_path || "", selectedLibraryRoots)}
                    />
                  </List.Item>
                )}
              />
            </Card>
          ) : (
          <Card
            title={detail ? t("pages.media_manager.edit_modal_title", { id: detail.id, title: detail.title || t("pages.media_manager.default_untitled") }) : t("pages.media_manager.default_edit_title")}
            loading={loadingDetail}
            extra={
              <Space>
                <Button onClick={() => (selectedId ? void loadDetail(selectedId) : undefined)} disabled={!selectedId}>
                  {t("pages.media_manager.btn_reset")}
                </Button>
                <Button type="primary" onClick={() => void onSave()} loading={saving} disabled={!selectedId}>
                  {t("pages.media_manager.btn_save")}
                </Button>
              </Space>
            }
          >
            <Form form={form} layout="vertical">
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="title" label={t("pages.media_manager.field_title")}>
                    <Input />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="original_title" label={t("pages.media_manager.field_original_title")}>
                    <Input />
                  </Form.Item>
                </Col>
              </Row>
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="status" label={t("pages.media_manager.field_status")}>
                    <Select
                      options={[
                        { value: "active", label: "active" },
                        { value: "inactive", label: "inactive" },
                        { value: "archived", label: "archived" },
                      ]}
                    />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="format" label={t("pages.media_manager.field_format")}>
                    <Input placeholder={t("pages.media_manager.format_placeholder")} />
                  </Form.Item>
                </Col>
              </Row>
              <Divider>{t("pages.media_manager.divider_scrape")}</Divider>
              <Form.Item name="overview" label={t("pages.media_manager.field_overview")}>
                <Input.TextArea rows={3} placeholder={t("pages.media_manager.overview_placeholder")} />
              </Form.Item>
              <Row gutter={12}>
                <Col span={8}>
                  <Form.Item name="rating" label={t("pages.media_manager.field_rating")}>
                    <InputNumber min={0} max={10} step={0.1} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={16}>
                  <Form.Item name="genres" label={t("pages.media_manager.field_genres")}>
                    <Input placeholder={t("pages.media_manager.genres_placeholder")} />
                  </Form.Item>
                </Col>
              </Row>
              <Row gutter={12}>
                <Col span={8}>
                  <Form.Item name="poster" label={t("pages.media_manager.field_poster")}>
                    <Input placeholder="https://..." />
                  </Form.Item>
                </Col>
                <Col span={8}>
                  <Form.Item name="backdrop" label={t("pages.media_manager.field_backdrop")}>
                    <Input placeholder="https://..." />
                  </Form.Item>
                </Col>
                <Col span={8}>
                  <Form.Item name="logo" label="Logo URL">
                    <Input placeholder="https://..." />
                  </Form.Item>
                </Col>
              </Row>
              <Row gutter={12}>
                <Col span={8}>
                  <Card size="small" title={t("pages.media_manager.card_poster_preview")}>
                    {posterPreview ? (
                      <Image src={posterPreview} alt="poster" width="100%" />
                    ) : (
                      <Avatar shape="square" style={{ width: "100%", height: 120 }} />
                    )}
                  </Card>
                </Col>
                <Col span={8}>
                  <Card size="small" title={t("pages.media_manager.card_backdrop_preview")}>
                    {backdropPreview ? (
                      <Image src={backdropPreview} alt="backdrop" width="100%" />
                    ) : (
                      <Avatar shape="square" style={{ width: "100%", height: 120 }} />
                    )}
                  </Card>
                </Col>
                <Col span={8}>
                  <Card size="small" title={t("pages.media_manager.card_logo_preview")}>
                    {logoPreview ? (
                      <Image src={logoPreview} alt="logo" width="100%" />
                    ) : (
                      <Avatar shape="square" style={{ width: "100%", height: 120 }} />
                    )}
                  </Card>
                </Col>
              </Row>
              <Alert
                type="info"
                showIcon
                style={{ marginBottom: 12 }}
                message={t("pages.media_manager.auto_sync_msg")}
              />
              <Row gutter={12}>
                <Col span={6}>
                  <Form.Item name="duration" label={t("pages.media_manager.field_duration")}>
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="width" label={t("pages.media_manager.field_width")}>
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="height" label={t("pages.media_manager.field_height")}>
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="bitrate" label={t("pages.media_manager.field_bitrate")}>
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
              </Row>
              <Form.Item
                name="meta_json"
                label={t("pages.media_manager.field_meta_json")}
                rules={[
                  {
                    validator: (_, value: string | undefined) => {
                      const raw = (value || "").trim();
                      if (!raw) return Promise.resolve();
                      try {
                        JSON.parse(raw);
                        return Promise.resolve();
                      } catch {
                        return Promise.reject(new Error(t("pages.media_manager.json_invalid_error")));
                      }
                    },
                  },
                ]}
              >
                <Input.TextArea rows={16} placeholder={t("pages.media_manager.meta_json_placeholder")} />
              </Form.Item>
            </Form>
          </Card>
          )}
        </Col>
      </Row>
    </Space>
  );
}
