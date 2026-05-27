import {
  AppstoreOutlined,
  DownloadOutlined,
  FolderOutlined,
  ReadOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { Breadcrumb, Button, Checkbox, Empty, Input, Select, Space, Spin, Tabs, Tree, message } from "antd";
import type { DataNode } from "antd/es/tree";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  DocumentFacet,
  DocumentItem,
  batchDownloadDocuments,
  documentCoverSrc,
  fetchDocumentFacets,
  fetchDocumentNodes,
  fetchDocuments,
  fetchRecentDocuments,
} from "../api/client";
import { useAuthStore } from "../store/auth";
import styles from "./DocumentBrowse.module.css";

type Props = {
  libraryId: number;
  libraryName?: string;
};

type ViewMode = "grid" | "list";
type SidebarTab = "tree" | "author" | "format" | "tag" | "year" | "recent";

const LIBRARY_ROOT_KEY = "__library_root__";

function displayNodeName(name: string) {
  return name.replace(/^\d+_/, "");
}

function updateTreeData(list: DataNode[], key: React.Key, children: DataNode[]): DataNode[] {
  return list.map((node) => {
    if (node.key === key) {
      return {
        ...node,
        children: children.length > 0 ? children : undefined,
        isLeaf: children.length === 0,
      };
    }
    if (node.children?.length) {
      return { ...node, children: updateTreeData(node.children, key, children) };
    }
    return node;
  });
}

async function loadFolderTreeNodes(libraryId: number, parent: string): Promise<DataNode[]> {
  const nodes = await fetchDocumentNodes(libraryId, parent);
  return nodes
    .filter((n) => n.node_type === "dir")
    .map((n) => ({
      key: n.path,
      title: displayNodeName(n.name || n.path.split("/").pop() || n.path),
      icon: <FolderOutlined />,
      isLeaf: false,
    }));
}

function formatSize(n?: number): string {
  if (!n || n <= 0) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export default function DocumentBrowse({ libraryId, libraryName }: Props) {
  const nav = useNavigate();
  const token = useAuthStore((s) => s.token);
  const [items, setItems] = useState<DocumentItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [q, setQ] = useState("");
  const [viewMode, setViewMode] = useState<ViewMode>("grid");
  const [sort, setSort] = useState("title");
  const [order, setOrder] = useState<"asc" | "desc">("asc");
  const [sidebarTab, setSidebarTab] = useState<SidebarTab>("tree");
  const [selectedFolder, setSelectedFolder] = useState("");
  const [treeData, setTreeData] = useState<DataNode[]>([]);
  const [treeLoading, setTreeLoading] = useState(false);
  const [expandedKeys, setExpandedKeys] = useState<React.Key[]>([LIBRARY_ROOT_KEY]);
  const [facets, setFacets] = useState<DocumentFacet[]>([]);
  const [filter, setFilter] = useState<{ kind?: string; value?: string }>({});
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [recent, setRecent] = useState<DocumentItem[]>([]);
  const [fullText, setFullText] = useState(false);
  const [downloading, setDownloading] = useState(false);

  const loadItems = useCallback(async () => {
    setLoading(true);
    try {
      const params: Record<string, string | number | boolean> = {
        sort,
        order,
        fulltext: fullText ? "1" : "0",
      };
      if (q.trim()) params.q = q.trim();
      if (filter.kind === "author" && filter.value) params.author = filter.value;
      if (filter.kind === "format" && filter.value) params.format = filter.value;
      if (filter.kind === "tag" && filter.value) params.tag = filter.value;
      if (filter.kind === "year" && filter.value) params.year = filter.value;
      if (selectedFolder && sidebarTab === "tree" && !filter.kind) params.parent = selectedFolder;
      const rows = await fetchDocuments(libraryId, params);
      setItems(rows);
    } catch {
      message.error("加载文档列表失败");
    } finally {
      setLoading(false);
    }
  }, [libraryId, q, sort, order, filter, selectedFolder, sidebarTab, fullText]);

  useEffect(() => {
    void loadItems();
  }, [loadItems]);

  const loadRootTree = useCallback(async () => {
    setTreeLoading(true);
    try {
      const children = await loadFolderTreeNodes(libraryId, "");
      setTreeData([
        {
          key: LIBRARY_ROOT_KEY,
          title: libraryName || "全部文档",
          icon: <FolderOutlined />,
          children,
          isLeaf: children.length === 0,
        },
      ]);
      setExpandedKeys([LIBRARY_ROOT_KEY]);
    } catch {
      setTreeData([]);
      message.error("加载目录树失败");
    } finally {
      setTreeLoading(false);
    }
  }, [libraryId, libraryName]);

  useEffect(() => {
    setSelectedFolder("");
    setFilter({});
    if (sidebarTab === "tree") {
      void loadRootTree();
    }
  }, [libraryId, sidebarTab, loadRootTree]);

  useEffect(() => {
    if (sidebarTab === "recent") {
      void fetchRecentDocuments(libraryId).then(setRecent).catch(() => setRecent([]));
    } else if (["author", "format", "tag", "year"].includes(sidebarTab)) {
      void fetchDocumentFacets(libraryId, sidebarTab).then(setFacets).catch(() => setFacets([]));
    }
  }, [libraryId, sidebarTab]);

  const breadcrumbParts = useMemo(() => {
    const parts = [{ label: libraryName || "图书馆", path: "" }];
    if (selectedFolder) {
      selectedFolder.split("/").forEach((seg, i, arr) => {
        parts.push({ label: displayNodeName(seg), path: arr.slice(0, i + 1).join("/") });
      });
    }
    return parts;
  }, [libraryName, selectedFolder]);

  const onTreeLoadData = async (node: DataNode) => {
    if (node.key === LIBRARY_ROOT_KEY || node.children?.length) return;
    try {
      const children = await loadFolderTreeNodes(libraryId, String(node.key));
      setTreeData((prev) => updateTreeData(prev, node.key, children));
    } catch {
      message.error("加载子目录失败");
    }
  };

  const toggleSelect = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const handleBatchDownload = async () => {
    if (selected.size === 0) {
      message.warning("请先选择文件");
      return;
    }
    setDownloading(true);
    try {
      const blob = await batchDownloadDocuments([...selected]);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `documents-${Date.now()}.zip`;
      a.click();
      URL.revokeObjectURL(url);
      message.success("下载已开始");
    } catch {
      message.error("打包下载失败");
    } finally {
      setDownloading(false);
    }
  };

  const openReader = (id: number) => nav(`/reader/${id}`);

  const renderGrid = () => (
    <div className={styles.grid}>
      {items.map((doc) => (
        <div key={doc.id} className={styles.card}>
          <div style={{ position: "absolute", zIndex: 1, padding: 6 }} onClick={(e) => e.stopPropagation()}>
            <Checkbox checked={selected.has(doc.id)} onChange={() => toggleSelect(doc.id)} />
          </div>
          <div className={styles.cover} onClick={() => openReader(doc.id)}>
            <img src={documentCoverSrc(doc.id, token)} alt="" loading="lazy" />
          </div>
          <div className={styles.cardBody} onClick={() => openReader(doc.id)}>
            <div className={styles.cardTitle}>{doc.title || "未命名"}</div>
            {doc.author && <div className={styles.cardMeta}>{doc.author}</div>}
            <span className={styles.formatBadge}>{doc.format || "doc"}</span>
          </div>
        </div>
      ))}
    </div>
  );

  const renderList = () => (
    <div>
      {items.map((doc) => (
        <div key={doc.id} className={styles.listRow} onClick={() => openReader(doc.id)}>
          <Checkbox checked={selected.has(doc.id)} onClick={(e) => e.stopPropagation()} onChange={() => toggleSelect(doc.id)} />
          <img className={styles.listThumb} src={documentCoverSrc(doc.id, token)} alt="" />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ fontWeight: 500 }}>{doc.title}</div>
            <div style={{ fontSize: 12, color: "rgba(255,255,255,0.5)" }}>
              {[doc.author, doc.format?.toUpperCase(), formatSize(doc.file_size)].filter(Boolean).join(" · ")}
            </div>
          </div>
          <Button type="link" icon={<ReadOutlined />} onClick={(e) => { e.stopPropagation(); openReader(doc.id); }}>
            阅读
          </Button>
        </div>
      ))}
    </div>
  );

  const sidebarContent = () => {
    if (sidebarTab === "tree") {
      if (treeLoading) {
        return <div className={styles.treeLoading}><Spin size="small" /></div>;
      }
      if (treeData.length === 0) {
        return (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="暂无目录，请先扫描媒体库"
            className={styles.treeEmpty}
          />
        );
      }
      return (
        <Tree
          blockNode
          showIcon
          className={styles.folderTree}
          treeData={treeData}
          expandedKeys={expandedKeys}
          selectedKeys={selectedFolder ? [selectedFolder] : [LIBRARY_ROOT_KEY]}
          onExpand={(keys) => setExpandedKeys(keys)}
          loadData={onTreeLoadData}
          onSelect={(keys) => {
            const key = keys[0] ? String(keys[0]) : LIBRARY_ROOT_KEY;
            if (key === LIBRARY_ROOT_KEY) {
              setSelectedFolder("");
            } else {
              setSelectedFolder(key);
            }
            setFilter({});
          }}
        />
      );
    }
    if (sidebarTab === "recent") {
      return recent.map((doc) => (
        <div key={doc.id} className={styles.facetItem} onClick={() => openReader(doc.id)}>
          <span>{doc.title}</span>
        </div>
      ));
    }
    return facets.map((f) => (
      <div
        key={f.name}
        className={`${styles.facetItem} ${filter.value === f.name ? styles.facetItemActive : ""}`}
        onClick={() => setFilter({ kind: sidebarTab, value: f.name })}
      >
        <span>{f.name}</span>
        <span style={{ opacity: 0.5 }}>{f.count}</span>
      </div>
    ));
  };

  return (
    <div className={styles.docBrowse}>
      <aside className={styles.sidebar}>
        <Tabs
          size="small"
          activeKey={sidebarTab}
          onChange={(k) => {
            setSidebarTab(k as SidebarTab);
            setFilter({});
          }}
          items={[
            { key: "tree", label: "目录" },
            { key: "author", label: "作者" },
            { key: "format", label: "格式" },
            { key: "tag", label: "标签" },
            { key: "year", label: "年份" },
            { key: "recent", label: "最近" },
          ]}
        />
        {sidebarContent()}
      </aside>

      <main className={styles.main}>
        <Breadcrumb
          className={styles.breadcrumb}
          items={breadcrumbParts.map((p, i) => ({
            title: i < breadcrumbParts.length - 1 ? (
              <a onClick={() => { setSelectedFolder(p.path); setFilter({}); }}>{p.label}</a>
            ) : p.label,
          }))}
        />

        <div className={styles.toolbar}>
          <Input.Search
            placeholder="搜索书名、作者…"
            allowClear
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onSearch={() => void loadItems()}
            style={{ width: 260 }}
          />
          <Checkbox checked={fullText} onChange={(e) => setFullText(e.target.checked)}>全文</Checkbox>
          <Select value={sort} onChange={setSort} style={{ width: 120 }} options={[
            { value: "title", label: "标题" },
            { value: "author", label: "作者" },
            { value: "size", label: "大小" },
            { value: "modified", label: "修改时间" },
            { value: "added", label: "添加时间" },
          ]} />
          <Select value={order} onChange={setOrder} style={{ width: 90 }} options={[
            { value: "asc", label: "升序" },
            { value: "desc", label: "降序" },
          ]} />
          <Space>
            <Button icon={<AppstoreOutlined />} type={viewMode === "grid" ? "primary" : "default"} onClick={() => setViewMode("grid")} />
            <Button icon={<UnorderedListOutlined />} type={viewMode === "list" ? "primary" : "default"} onClick={() => setViewMode("list")} />
          </Space>
          <Button icon={<DownloadOutlined />} loading={downloading} onClick={() => void handleBatchDownload()}>
            批量下载 ({selected.size})
          </Button>
        </div>

        {loading ? (
          <div style={{ textAlign: "center", padding: 48 }}><Spin /></div>
        ) : items.length === 0 ? (
          <div className={styles.emptyWrap}><Empty description="没有找到匹配的文档" /></div>
        ) : viewMode === "grid" ? renderGrid() : renderList()}
      </main>
    </div>
  );
}
