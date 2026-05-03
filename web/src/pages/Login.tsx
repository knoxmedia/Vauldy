import { Button, Card, Form, Input, Typography, message } from "antd";
import { useNavigate, useSearchParams } from "react-router-dom";
import { fetchUserInfo, login } from "../api/client";
import { useAuthStore, type UserRole } from "../store/auth";

const { Paragraph, Title } = Typography;

export default function LoginPage() {
  const [form] = Form.useForm();
  const nav = useNavigate();
  const [params] = useSearchParams();
  const setToken = useAuthStore((s) => s.setToken);
  const setProfile = useAuthStore((s) => s.setProfile);

  return (
    <div
      style={{
        minHeight: "100vh",
        width: "100%",
        background: "linear-gradient(160deg, #0f1419 0%, #1a2332 100%)",
      }}
    >
      <div
        className="app-main-centered"
        style={{
          minHeight: "100vh",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          paddingTop: 24,
          paddingBottom: 24,
          boxSizing: "border-box",
        }}
      >
        <Card
          style={{ width: "100%", maxWidth: 400 }}
          title={<Title level={4} style={{ margin: 0 }}>登录 Knox-Media</Title>}
        >
          <Paragraph type="secondary" style={{ marginBottom: 16 }}>
            登录后可浏览媒体与播放。管理员可进行媒体库、上传与控制台操作。
          </Paragraph>
          <Form
            form={form}
            layout="vertical"
            onFinish={async (v: { username: string; password: string }) => {
              try {
                const t = await login(v.username, v.password);
                setToken(t);
                const u = await fetchUserInfo();
                setProfile(u.username, u.role as UserRole);
                message.success("登录成功");
                const redir = params.get("redirect");
                nav(redir && redir.startsWith("/") ? redir : "/", { replace: true });
              } catch {
                message.error("用户名或密码错误");
              }
            }}
          >
            <Form.Item name="username" label="用户名" rules={[{ required: true, message: "请输入用户名" }]}>
              <Input autoComplete="username" size="large" placeholder="admin 或 viewer" />
            </Form.Item>
            <Form.Item name="password" label="密码" rules={[{ required: true, message: "请输入密码" }]}>
              <Input.Password autoComplete="current-password" size="large" />
            </Form.Item>
            <Button type="primary" htmlType="submit" size="large" block>
              登录
            </Button>
          </Form>
          <Paragraph type="secondary" style={{ marginTop: 16, fontSize: 12 }}>
            演示账号：管理员 <code>admin</code> / <code>admin123</code>；普通用户 <code>viewer</code> /{" "}
            <code>viewer123</code>
          </Paragraph>
        </Card>
      </div>
    </div>
  );
}
