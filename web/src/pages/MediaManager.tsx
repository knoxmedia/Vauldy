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
  const full = normalizePath(fullPath).replace(/\/+$/, "");
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

function nodeTitle(name: string, kind: "dir" | "file") {
  return <span>{kind === "dir" ? `📁 ${name}` : `🎬 ${name}`}</span>;
}

export default function MediaManagerPage() {
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
    if (!libraryId && items.length > 0) {
      setLibraryId(items[0].id);
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
        path: toLibraryRelativePath(first.file_path || "", selectedLibraryRoots),
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
    void loadLibraries().catch((e: unknown) => message.error((e as Error).message || "加载媒体库失败"));
  }, []);

  useEffect(() => {
    void loadMedia(libraryId).catch((e: unknown) => message.error((e as Error).message || "加载媒体失败"));
  }, [libraryId]);

  useEffect(() => {
    if (!selectedId) return;
    void loadDetail(selectedId).catch((e: unknown) => message.error((e as Error).message || "加载媒体详情失败"));
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
      const rel = toLibraryRelativePath(m.file_path || "", selectedLibraryRoots);
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
        const p = toLibraryRelativePath(x.file_path || "", selectedLibraryRoots);
        return p.startsWith(prefix) && p !== selectedNode.path;
      })
      .sort((a, b) =>
        toLibraryRelativePath(a.file_path || "", selectedLibraryRoots).localeCompare(
          toLibraryRelativePath(b.file_path || "", selectedLibraryRoots)
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
      message.success("媒体资料已保存");
      await loadMedia(libraryId);
      await loadDetail(selectedId);
    } catch (e: unknown) {
      message.error((e as Error).message || "保存失败");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        左侧树展示媒体库目录与文件；点击目录显示目录信息，点击文件可编辑并保存元数据。
      </Typography.Paragraph>

      <Row gutter={16}>
        <Col xs={24} lg={11}>
          <Card
            title="媒体库树"
            extra={
              <Select
                style={{ width: 220 }}
                placeholder="选择媒体库"
                value={libraryId}
                onChange={(v) => setLibraryId(v)}
                options={libs.map((l) => ({ value: l.id, label: l.name }))}
              />
            }
          >
            <Input
              allowClear
              placeholder="按文件名或路径过滤"
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
            <Card title={`目录信息 - ${selectedNode.name}`}>
              <Descriptions column={1} bordered size="small">
                <Descriptions.Item label="目录名称">{selectedNode.name}</Descriptions.Item>
                <Descriptions.Item label="目录路径">{selectedNode.path}</Descriptions.Item>
                <Descriptions.Item label="包含文件数">
                  {rows.filter((x) => toLibraryRelativePath(x.file_path || "", selectedLibraryRoots).startsWith(selectedNode.path)).length}
                </Descriptions.Item>
              </Descriptions>
              <Collapse
                size="small"
                style={{ marginTop: 12 }}
                items={[
                  {
                    key: "debug-root",
                    label: "排障信息（仅管理员可见）",
                    children: (
                      <Descriptions column={1} bordered size="small">
                        <Descriptions.Item label="媒体库根路径">
                          {selectedLibrary?.path || "-"}
                        </Descriptions.Item>
                      </Descriptions>
                    ),
                  },
                ]}
              />
              <Divider />
              <Space style={{ marginBottom: 8 }}>
                <Typography.Text strong>目录内文件</Typography.Text>
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
                            path: toLibraryRelativePath(first.file_path || "", selectedLibraryRoots),
                      mediaId: first.id,
                    });
                  }}
                >
                  编辑第一条
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
                            path: toLibraryRelativePath(item.file_path || "", selectedLibraryRoots),
                            mediaId: item.id,
                          })
                        }
                      >
                        编辑
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
                            path: toLibraryRelativePath(next.file_path || "", selectedLibraryRoots),
                            mediaId: next.id,
                          });
                        }}
                      >
                        下一条
                      </Button>,
                    ]}
                  >
                    <List.Item.Meta
                      title={item.title || item.file_id}
                      description={toLibraryRelativePath(item.file_path || "", selectedLibraryRoots)}
                    />
                  </List.Item>
                )}
              />
            </Card>
          ) : (
          <Card
            title={detail ? `编辑 #${detail.id} - ${detail.title || "未命名"}` : "编辑媒体资料"}
            loading={loadingDetail}
            extra={
              <Space>
                <Button onClick={() => (selectedId ? void loadDetail(selectedId) : undefined)} disabled={!selectedId}>
                  重置
                </Button>
                <Button type="primary" onClick={() => void onSave()} loading={saving} disabled={!selectedId}>
                  保存
                </Button>
              </Space>
            }
          >
            <Form form={form} layout="vertical">
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="title" label="标题">
                    <Input />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="original_title" label="原始标题">
                    <Input />
                  </Form.Item>
                </Col>
              </Row>
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="status" label="状态">
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
                  <Form.Item name="format" label="容器/格式">
                    <Input placeholder="例如 mov,mp4,m4a,3gp,3g2,mj2" />
                  </Form.Item>
                </Col>
              </Row>
              <Divider>刮削资料（结构化）</Divider>
              <Form.Item name="overview" label="简介">
                <Input.TextArea rows={3} placeholder="影片简介" />
              </Form.Item>
              <Row gutter={12}>
                <Col span={8}>
                  <Form.Item name="rating" label="评分">
                    <InputNumber min={0} max={10} step={0.1} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={16}>
                  <Form.Item name="genres" label="类型（逗号分隔）">
                    <Input placeholder="动作, 科幻, 冒险" />
                  </Form.Item>
                </Col>
              </Row>
              <Row gutter={12}>
                <Col span={8}>
                  <Form.Item name="poster" label="海报URL">
                    <Input placeholder="https://..." />
                  </Form.Item>
                </Col>
                <Col span={8}>
                  <Form.Item name="backdrop" label="背景URL">
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
                  <Card size="small" title="海报预览">
                    {posterPreview ? (
                      <Image src={posterPreview} alt="poster" width="100%" />
                    ) : (
                      <Avatar shape="square" style={{ width: "100%", height: 120 }} />
                    )}
                  </Card>
                </Col>
                <Col span={8}>
                  <Card size="small" title="背景预览">
                    {backdropPreview ? (
                      <Image src={backdropPreview} alt="backdrop" width="100%" />
                    ) : (
                      <Avatar shape="square" style={{ width: "100%", height: 120 }} />
                    )}
                  </Card>
                </Col>
                <Col span={8}>
                  <Card size="small" title="Logo 预览">
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
                message="结构化字段会自动同步到 meta_json.scrape，无需手动维护 JSON。"
              />
              <Row gutter={12}>
                <Col span={6}>
                  <Form.Item name="duration" label="时长(秒)">
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="width" label="宽度">
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="height" label="高度">
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="bitrate" label="码率">
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
              </Row>
              <Form.Item
                name="meta_json"
                label="完整元数据(JSON)"
                rules={[
                  {
                    validator: (_, value: string | undefined) => {
                      const raw = (value || "").trim();
                      if (!raw) return Promise.resolve();
                      try {
                        JSON.parse(raw);
                        return Promise.resolve();
                      } catch {
                        return Promise.reject(new Error("JSON 格式无效"));
                      }
                    },
                  },
                ]}
              >
                <Input.TextArea rows={16} placeholder='例如 {"scrape":{"overview":"..."}}' />
              </Form.Item>
            </Form>
          </Card>
          )}
        </Col>
      </Row>
    </Space>
  );
}
