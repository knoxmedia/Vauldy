import { Button, Space, Table, Typography, message } from "antd";
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { MediaItem, fetchFavorites, removeFavorite } from "../api/client";

export default function FavoritesPage() {
  const [rows, setRows] = useState<MediaItem[]>([]);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setRows(await fetchFavorites());
    } catch (e: unknown) {
      message.error((e as Error).message || "加载失败");
      setRows([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function onUnfavorite(id: number) {
    try {
      await removeFavorite(id);
      message.success("已取消收藏");
      void load();
    } catch (e: unknown) {
      message.error((e as Error).message || "操作失败");
    }
  }

  return (
    <div style={{ padding: "16px 0 32px" }}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 16 }}>
        当前登录用户收藏的视频；在播放页可点击「收藏」添加。
      </Typography.Paragraph>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={rows}
        pagination={{ pageSize: 20 }}
        locale={{ emptyText: "暂无收藏，去浏览媒体并加入收藏吧" }}
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
            render: (_, r) =>
              r.width && r.height ? `${r.width}x${r.height}` : "—",
          },
          {
            title: "操作",
            key: "op",
            width: 180,
            render: (_, r) => (
              <Space>
                <Link to={`/player/${r.id}`}>播放</Link>
                <Button type="link" danger size="small" onClick={() => void onUnfavorite(r.id)}>
                  取消收藏
                </Button>
              </Space>
            ),
          },
        ]}
      />
    </div>
  );
}
