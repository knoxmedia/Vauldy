import { Button, Input, Space, Table, Typography, message } from "antd";
import { SearchOutlined } from "@ant-design/icons";
import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { MediaItem, fetchMedia } from "../api/client";
import { useT } from "../i18n";

export default function SearchPage() {
  const nav = useNavigate();
  const t = useT();
  const [searchParams] = useSearchParams();
  const qParam = searchParams.get("q")?.trim() ?? "";
  const [keyword, setKeyword] = useState(qParam);
  const [rows, setRows] = useState<MediaItem[]>([]);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    setKeyword(qParam);
  }, [qParam]);

  useEffect(() => {
    setLoading(true);
    void fetchMedia(undefined, { limit: 500 })
      .then((items) => setRows(items))
      .catch((e: unknown) => {
        message.error((e as Error).message || t("pages.search.load_failed"));
        setRows([]);
      })
      .finally(() => setLoading(false));
  }, [t]);

  const displayRows = useMemo(() => {
    const q = qParam.toLowerCase();
    if (!q) return rows;
    return rows.filter((r) => (r.title ?? "").toLowerCase().includes(q));
  }, [rows, qParam]);

  const doSearch = () => {
    const v = keyword.trim();
    nav(v ? `/search?q=${encodeURIComponent(v)}` : "/search");
  };

  return (
    <div style={{ padding: "16px 0 32px" }}>
      <Typography.Title level={3} style={{ color: "#fff", marginTop: 0 }}>
        {t("pages.search.title")}
      </Typography.Title>
      <Space wrap style={{ marginBottom: 16 }}>
        <Input
          allowClear
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          onPressEnter={doSearch}
          prefix={<SearchOutlined style={{ color: "#666" }} />}
          placeholder={t("pages.search.keyword_placeholder")}
          style={{ width: 320 }}
        />
        <Button type="primary" onClick={doSearch}>
          {t("pages.search.search_btn")}
        </Button>
        <Button onClick={() => nav("/search")}>{t("pages.search.clear_btn")}</Button>
      </Space>

      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {qParam ? t("pages.search.current_keyword", { q: qParam }) : t("pages.search.empty_hint")}
      </Typography.Paragraph>

      <Table
        rowKey="id"
        loading={loading}
        dataSource={displayRows}
        pagination={{ pageSize: 20 }}
        locale={{ emptyText: qParam ? t("pages.search.no_match") : t("pages.search.start_hint") }}
        columns={[
          { title: t("pages.search.col_id"), dataIndex: "id", width: 70 },
          { title: t("pages.search.col_title"), dataIndex: "title" },
          { title: t("pages.search.col_type"), dataIndex: "file_type", width: 90 },
          {
            title: t("pages.search.col_duration_s"),
            dataIndex: "duration",
            width: 90,
            render: (v: number) => v || "—",
          },
          {
            title: t("pages.search.col_resolution"),
            width: 120,
            render: (_, r) => (r.width && r.height ? `${r.width}x${r.height}` : "—"),
          },
          {
            title: t("pages.search.col_op"),
            key: "op",
            width: 120,
            render: (_, r) => <Link to={`/player/${r.id}`}>{t("pages.search.play")}</Link>,
          },
        ]}
      />
    </div>
  );
}
