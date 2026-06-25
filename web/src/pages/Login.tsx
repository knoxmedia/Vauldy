import { Button, Card, Form, Input, Typography, message } from "antd";
import { useNavigate, useSearchParams } from "react-router-dom";
import { fetchUserInfo, login } from "../api/client";
import { useAppName } from "../store/branding";
import { useT } from "../i18n";
import { defaultPlayerPrefs, normalizePlayerPrefs } from "../lib/playerPrefs";
import { useAuthStore, type UserRole } from "../store/auth";

const { Paragraph, Title } = Typography;

export default function LoginPage() {
  const [form] = Form.useForm();
  const nav = useNavigate();
  const [params] = useSearchParams();
  const setToken = useAuthStore((s) => s.setToken);
  const setProfile = useAuthStore((s) => s.setProfile);
  const t = useT();
  const appName = useAppName();

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
          title={<Title level={4} style={{ margin: 0 }}>{t("login.title", { appName })}</Title>}
        >
          <Paragraph type="secondary" style={{ marginBottom: 16 }}>
            {t("login.subtitle")}
          </Paragraph>
          <Form
            form={form}
            layout="vertical"
            onFinish={async (v: { username: string; password: string }) => {
              try {
                const tk = await login(v.username, v.password);
                setToken(tk);
                const u = await fetchUserInfo();
                setProfile(u.username, u.role as UserRole, {
                  canPlay: u.can_play !== false,
                  avatarUrl: u.avatar_url || null,
                  uiLocale: u.ui_locale || null,
                  playerPrefs: u.player_prefs ? normalizePlayerPrefs(u.player_prefs) : defaultPlayerPrefs(),
                });
                message.success(t("login.success"));
                const redir = params.get("redirect");
                nav(redir && redir.startsWith("/") ? redir : "/", { replace: true });
              } catch {
                message.error(t("login.failure"));
              }
            }}
          >
            <Form.Item name="username" label={t("login.username")} rules={[{ required: true, message: t("login.username_required") }]}>
              <Input autoComplete="username" size="large" placeholder={t("login.username_placeholder")} />
            </Form.Item>
            <Form.Item name="password" label={t("login.password")} rules={[{ required: true, message: t("login.password_required") }]}>
              <Input.Password autoComplete="current-password" size="large" />
            </Form.Item>
            <Button type="primary" htmlType="submit" size="large" block>
              {t("login.submit")}
            </Button>
          </Form>
          <Paragraph type="secondary" style={{ marginTop: 16, fontSize: 12 }}>
            {t("login.demo_hint", {
              admin_user: "admin",
              admin_pass: "admin123",
              viewer_user: "viewer",
              viewer_pass: "viewer123",
            })}
          </Paragraph>
        </Card>
      </div>
    </div>
  );
}
