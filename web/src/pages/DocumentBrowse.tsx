import {
  AppstoreOutlined,
  DeleteOutlined,
  DownloadOutlined,
  FolderOutlined,
  ReadOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { Breadcrumb, Button, Checkbox, Empty, Input, Modal, Select, Space, Spin, Tabs, Tree, message } from "antd";
import type { DataNode } from "antd/es/tree";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  DocumentFacet,
  DocumentItem,
  batchDownloadDocuments,
  deleteMedia,
  documentCoverSrc,
  fetchDocumentFacets,
  fetchDocumentNodes,
  fetchDocuments,
  fetchRecentDocuments,
} from "../api/client";
import { useAuthStore } from "../store/auth";
import { useT } from "../i18n";
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
  const t = useT();
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
  const [deleting, setDeleting] = useState(false);

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
      message.error(t("pages.document_browse.load_failed"));
    } finally {
      setLoading(false);
    }
  }, [libraryId, q, sort, order, filter, selectedFolder, sidebarTab, fullText, t]);

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
          title: libraryName || t("pages.document_browse.all_documents_fallback"),
          icon: <FolderOutlined />,
          children,
          isLeaf: children.length === 0,
        },
      ]);
      setExpandedKeys([LIBRARY_ROOT_KEY]);
    } catch {
      setTreeData([]);
      message.error(t("pages.document_browse.load_tree_failed"));
    } finally {
      setTreeLoading(false);
    }
  }, [libraryId, libraryName, t]);

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
    const parts = [{ label: libraryName || t("pages.document_browse.library_fallback"), path: "" }];
    if (selectedFolder) {
      selectedFolder.split("/").forEach((seg, i, arr) => {
        parts.push({ label: displayNodeName(seg), path: arr.slice(0, i + 1).join("/") });
      });
    }
    return parts;
  }, [libraryName, selectedFolder, t]);

  const onTreeLoadData = async (node: DataNode) => {
    if (node.key === LIBRARY_ROOT_KEY || node.children?.length) return;
    try {
      const children = await loadFolderTreeNodes(libraryId, String(node.key));
      setTreeData((prev) => updateTreeData(prev, node.key, children));
    } catch {
      message.error(t("pages.document_browse.load_subtree_failed"));
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
      message.warning(t("pages.document_browse.select_file_first"));
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
      message.success(t("pages.document_browse.download_started"));
    } catch {
      message.error(t("pages.document_browse.download_failed"));
    } finally {
      setDownloading(false);
    }
  };

  const handleBatchDelete = () => {
    if (selected.size === 0) {
      message.warning(t("pages.document_browse.select_file_first"));
      return;
    }
    const ids = [...selected];
    Modal.confirm({
      title: t("pages.document_browse.batch_delete_title", { count: ids.length }),
      centered: true,
      okText: t("components.media_menu.ok"),
      cancelText: t("components.media_menu.cancel"),
      okButtonProps: { danger: true },
      content: t("pages.document_browse.batch_delete_confirm"),
      onOk: async () => {
        setDeleting(true);
        let ok = 0;
        let fail = 0;
        try {
          for (const id of ids) {
            try {
              await deleteMedia(id);
              ok++;
            } catch {
              fail++;
            }
          }
          setSelected(new Set());
          await loadItems();
          if (ok > 0) {
            message.success(
              fail > 0
                ? t("pages.document_browse.batch_deleted_with_skip", { ok, fail })
                : t("pages.document_browse.batch_deleted", { ok }),
            );
          } else {
            message.error(t("components.media_menu.delete_failed"));
          }
        } finally {
          setDeleting(false);
        }
      },
    });
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
            <div className={styles.cardTitle}>{doc.title || t("pages.document_browse.untitled")}</div>
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
            {t("pages.document_browse.read")}
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
            description={t("pages.document_browse.no_tree")}
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
            { key: "tree", label: t("pages.document_browse.tab_tree") },
            { key: "author", label: t("pages.document_browse.tab_author") },
            { key: "format", label: t("pages.document_browse.tab_format") },
            { key: "tag", label: t("pages.document_browse.tab_tag") },
            { key: "year", label: t("pages.document_browse.tab_year") },
            { key: "recent", label: t("pages.document_browse.tab_recent") },
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
            placeholder={t("pages.document_browse.search_placeholder")}
            allowClear
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onSearch={() => void loadItems()}
            style={{ width: 260 }}
          />
          <Checkbox checked={fullText} onChange={(e) => setFullText(e.target.checked)}>{t("pages.document_browse.fulltext")}</Checkbox>
          <Select value={sort} onChange={setSort} style={{ width: 120 }} options={[
            { value: "title", label: t("pages.document_browse.sort_title") },
            { value: "author", label: t("pages.document_browse.sort_author") },
            { value: "size", label: t("pages.document_browse.sort_size") },
            { value: "modified", label: t("pages.document_browse.sort_modified") },
            { value: "added", label: t("pages.document_browse.sort_added") },
          ]} />
          <Select value={order} onChange={setOrder} style={{ width: 90 }} options={[
            { value: "asc", label: t("pages.document_browse.order_asc") },
            { value: "desc", label: t("pages.document_browse.order_desc") },
          ]} />
          <Space>
            <Button icon={<AppstoreOutlined />} type={viewMode === "grid" ? "primary" : "default"} onClick={() => setViewMode("grid")} />
            <Button icon={<UnorderedListOutlined />} type={viewMode === "list" ? "primary" : "default"} onClick={() => setViewMode("list")} />
          </Space>
          <Button icon={<DownloadOutlined />} loading={downloading} onClick={() => void handleBatchDownload()}>
            {t("pages.document_browse.batch_download", { count: selected.size })}
          </Button>
          {selected.size > 0 ? (
            <Button
              danger
              icon={<DeleteOutlined />}
              loading={deleting}
              onClick={() => handleBatchDelete()}
            >
              {t("pages.document_browse.batch_delete", { count: selected.size })}
            </Button>
          ) : null}
        </div>

        {loading ? (
          <div style={{ textAlign: "center", padding: 48 }}><Spin /></div>
        ) : items.length === 0 ? (
          <div className={styles.emptyWrap}><Empty description={t("pages.document_browse.no_results")} /></div>
        ) : viewMode === "grid" ? renderGrid() : renderList()}
      </main>
    </div>
  );
}
