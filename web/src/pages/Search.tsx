import { Button, Input, Space, Table, Typography, message } from "antd";
import { SearchOutlined } from "@ant-design/icons";
import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { MediaItem, fetchMedia } from "../api/client";

export default function SearchPage() {
  const nav = useNavigate();
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
        message.error((e as Error).message || "加载失败");
        setRows([]);
      })
      .finally(() => setLoading(false));
  }, []);

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
        搜索
      </Typography.Title>
      <Space wrap style={{ marginBottom: 16 }}>
        <Input
          allowClear
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          onPressEnter={doSearch}
          prefix={<SearchOutlined style={{ color: "#666" }} />}
          placeholder="输入标题关键字"
          style={{ width: 320 }}
        />
        <Button type="primary" onClick={doSearch}>
          搜索
        </Button>
        <Button onClick={() => nav("/search")}>清空</Button>
      </Space>

      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {qParam ? `当前关键字：${qParam}` : "请输入关键字进行搜索"}
      </Typography.Paragraph>

      <Table
        rowKey="id"
        loading={loading}
        dataSource={displayRows}
        pagination={{ pageSize: 20 }}
        locale={{ emptyText: qParam ? "没有匹配结果" : "输入关键字后开始搜索" }}
        columns={[
          { title: "ID", dataIndex: "id", width: 70 },
          { title: "标题", dataIndex: "title" },
          { title: "类型", dataIndex: "file_type", width: 90 },
          {
            title: "时长(s)",
            dataIndex: "duration",
            width: 90,
            render: (v: number) => v || "—",
          },
          {
            title: "分辨率",
            width: 120,
            render: (_, r) => (r.width && r.height ? `${r.width}x${r.height}` : "—"),
          },
          {
            title: "操作",
            key: "op",
            width: 120,
            render: (_, r) => <Link to={`/player/${r.id}`}>播放</Link>,
          },
        ]}
      />
    </div>
  );
}
