import { Card, Space, Typography } from "antd";
import { isAdminRole, useAuthStore } from "../store/auth";

const { Paragraph } = Typography;

export default function SettingsPage() {
  const username = useAuthStore((s) => s.username);
  const role = useAuthStore((s) => s.role);

  return (
    <Space direction="vertical" size="large" className="app-narrow-block" style={{ display: "flex", width: "100%" }}>
      <Card title="账号信息">
        <Paragraph>
          用户名：<strong>{username || "—"}</strong>
        </Paragraph>
        <Paragraph>
          角色：
          <strong>{isAdminRole(role) ? "管理员" : "普通用户"}</strong>
        </Paragraph>
        <Paragraph type="secondary">
          {isAdminRole(role)
            ? "管理员可使用侧边栏「媒体库」「上传」「控制台」进行管理与运维。"
            : "您可「浏览媒体」「我的收藏」与播放；媒体库与上传由管理员维护。"}
        </Paragraph>
      </Card>
      <Card title="安全提示">
        <Paragraph type="secondary">
          生产环境请在服务器 <code>config.yml</code> 中修改 JWT 密钥；勿共享管理员账号。
        </Paragraph>
      </Card>
    </Space>
  );
}
