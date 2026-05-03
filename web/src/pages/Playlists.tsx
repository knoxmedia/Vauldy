import { Empty } from "antd";

/** 播放列表占位页，后续可对接后端播放列表 API */
export default function PlaylistsPage() {
  return (
    <div style={{ padding: "16px 0 32px" }}>
      <Empty description="播放列表功能开发中" style={{ color: "#888" }} />
    </div>
  );
}
