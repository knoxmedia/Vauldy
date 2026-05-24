import {
  Avatar,
  Button,
  Dropdown,
  Layout,
  Tooltip,
  Drawer,
  Space,
  Typography,
} from "antd";
import type { MenuProps } from "antd";
import {
  CloseOutlined,
  MenuFoldOutlined,
  MenuOutlined,
  MenuUnfoldOutlined,
  PlayCircleOutlined,
  PushpinOutlined,
  SettingOutlined,
  SlidersOutlined,
} from "@ant-design/icons";
import {
  Link,
  Navigate,
  Outlet,
  Route,
  Routes,
  useLocation,
  useNavigate,
} from "react-router-dom";
import { useEffect, useState } from "react";
import HomePage from "./pages/Home";
import LibraryPage from "./pages/Library";
import BrowsePage from "./pages/Browse";
import FavoritesPage from "./pages/Favorites";
import SeriesDetailPage from "./pages/SeriesDetail";
import PlayerPage from "./pages/Player";
import UploadPage from "./pages/Upload";
import SettingsPage from "./pages/Settings";
import LoginPage from "./pages/Login";
import AdminConsolePage from "./pages/AdminConsole";
import MediaManagerPage from "./pages/MediaManager";
import TaskManagerPage from "./pages/TaskManager";
import DRMLicenseAuditPage from "./pages/DRMLicenseAudit";
import AccessLogsPage from "./pages/AccessLogs";
import ApiCredentialsPage from "./pages/ApiCredentials";
import UsersPage from "./pages/Users";
import PlaylistsPage from "./pages/Playlists";
import SearchPage from "./pages/Search";
import MediaDetailPage from "./pages/MediaDetail";
import PlaybackHistoryPage from "./pages/PlaybackHistory";
import ScrapeConfigPage from "./pages/ScrapeConfig";
import AIProviderPage from "./pages/AIProvider";
import SystemOptionsPage from "./pages/SystemOptions";
import RequireAuth from "./routes/RequireAuth";
import RequireAdmin from "./routes/RequireAdmin";
import { fetchUserInfo, logout } from "./api/client";
import { defaultPlayerPrefs, normalizePlayerPrefs } from "./lib/playerPrefs";
import { isAdminRole, useAuthStore } from "./store/auth";
import MainNav from "./components/MainNav";

const { Header, Content, Sider } = Layout;

const SIDEBAR_MODE_KEY = "knox-media-sidebar-mode";

type SidebarMode = "expanded" | "collapsed" | "hidden";

function readSidebarMode(): SidebarMode {
  try {
    const v = localStorage.getItem(SIDEBAR_MODE_KEY);
    if (v === "expanded" || v === "collapsed" || v === "hidden") return v;
  } catch {
    /* ignore */
  }
  return "expanded";
}

function LegacyMediaToBrowse() {
  const { search } = useLocation();
  return <Navigate to={`/browse${search}`} replace />;
}

function ProfileSync() {
  const token = useAuthStore((s) => s.token);
  const setProfile = useAuthStore((s) => s.setProfile);
  const clearSession = useAuthStore((s) => s.clearSession);

  useEffect(() => {
    if (!token) return;
    void fetchUserInfo()
      .then((u) =>
        setProfile(u.username, u.role, {
          canPlay: u.can_play !== false,
          avatarUrl: u.avatar_url || null,
          uiLocale: u.ui_locale || null,
          playerPrefs: u.player_prefs ? normalizePlayerPrefs(u.player_prefs) : defaultPlayerPrefs(),
        })
      )
      .catch(() => clearSession());
  }, [token, setProfile, clearSession]);

  return null;
}

function MainShell() {
  const loc = useLocation();
  const nav = useNavigate();
  const role = useAuthStore((s) => s.role);
  const username = useAuthStore((s) => s.username);
  const avatarUrl = useAuthStore((s) => s.avatarUrl);
  const clearSession = useAuthStore((s) => s.clearSession);
  const admin = isAdminRole(role);
  const isPlayerRoute = loc.pathname.startsWith("/player");
  const isHomeRoute = loc.pathname === "/" || loc.pathname === "";

  const [mode, setModeState] = useState<SidebarMode>(() => readSidebarMode());
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [autoCollapseOnLeave, setAutoCollapseOnLeave] = useState(false);

  const setMode = (m: SidebarMode) => {
    setModeState(m);
    if (m === "hidden") {
      setAutoCollapseOnLeave(false);
    }
    try {
      localStorage.setItem(SIDEBAR_MODE_KEY, m);
    } catch {
      /* ignore */
    }
  };

  const siderCollapsed = mode === "collapsed";

  const userMenuItems: MenuProps["items"] = [
    {
      key: "who",
      label: (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {username || "用户"}
          {role ? ` · ${role === "admin" ? "管理员" : "用户"}` : ""}
        </Typography.Text>
      ),
      disabled: true,
    },
    { type: "divider" },
    {
      key: "settings",
      icon: <SlidersOutlined />,
      label: "账号设置",
      onClick: () => nav("/settings"),
    },
    { type: "divider" },
    {
      key: "logout",
      danger: true,
      label: "退出",
      onClick: async () => {
        try {
          await logout();
        } catch {
          // ignore logout API errors and still clear local session
        } finally {
          clearSession();
          nav("/login", { replace: true });
        }
      },
    },
  ];

  const pathTitle = (() => {
    const p = loc.pathname;
    if (p === "/" || p === "") return "";
    if (p.startsWith("/favorites")) return "我的收藏";
    if (p.startsWith("/playlists")) return "播放列表";
    if (p.startsWith("/search")) return "搜索";
    if (p.startsWith("/browse")) return "浏览媒体";
    if (p.startsWith("/series")) return "剧集详情";
    if (p.startsWith("/playback-history")) return "播放历史";
    if (p.startsWith("/player")) return "播放";
    if (p.startsWith("/settings")) return "账号";
    if (p.startsWith("/library")) return "媒体库";
    if (p.startsWith("/upload")) return "上传";
    if (p.startsWith("/media-manager")) return "媒体资料管理";
    if (p.startsWith("/tasks")) return "任务管理";
    if (p.startsWith("/drm-license-audit")) return "DRM许可证审计";
    if (p.startsWith("/access-logs")) return "访问日志";
    if (p.startsWith("/api-credentials")) return "API 凭证";
    if (p.startsWith("/users")) return "用户管理";
    if (p.startsWith("/console")) return "控制台";
    if (p.startsWith("/system-options")) return "系统选项";
    if (p.startsWith("/scrape-config")) return "元数据提供者";
    if (p.startsWith("/ai-provider")) return "AI 提供商";
    return "";
  })();

  return (
    <Layout className="app-shell" style={{ minHeight: "100vh", background: "#000" }}>
      <ProfileSync />
      {!isPlayerRoute && mode !== "hidden" && (
        <Sider
          className="app-shell-sider"
          width={260}
          collapsedWidth={64}
          collapsed={siderCollapsed}
          collapsible
          onCollapse={(c) => {
            if (c) {
              setMode("collapsed");
              return;
            }
            setAutoCollapseOnLeave(true);
            setMode("expanded");
          }}
          theme="dark"
          trigger={
            siderCollapsed ? (
              <MenuUnfoldOutlined style={{ color: "#fff" }} />
            ) : (
              <MenuFoldOutlined style={{ color: "#fff" }} />
            )
          }
          onMouseLeave={() => {
            if (autoCollapseOnLeave && mode === "expanded") {
              setMode("collapsed");
            }
          }}
        >
          <div className="app-sider-brand">
            {siderCollapsed ? (
              <Button
                type="text"
                size="small"
                className="app-sider-collapsed-open-btn"
                icon={<MenuOutlined style={{ color: "#aaa" }} />}
                onClick={() => {
                  setAutoCollapseOnLeave(true);
                  setMode("expanded");
                }}
                aria-label="展开导航栏"
              />
            ) : (
              <>
                <Button
                  type="text"
                  size="small"
                  className="app-sider-mode-btn"
                  icon={<MenuOutlined style={{ color: "#aaa" }} />}
                  onClick={() => {
                    setAutoCollapseOnLeave(false);
                    setMode("collapsed");
                  }}
                  aria-label="仅图标显示"
                />
                <Link to="/" className="app-sider-logo" title="首页">
                  <>
                    <span>Knox-Media</span>
                  </>
                </Link>
                <Tooltip title="关闭侧边栏">
                  <Button
                    type="text"
                    size="small"
                    className="app-sider-close-btn"
                    icon={<CloseOutlined style={{ color: "#aaa" }} />}
                    onClick={() => setMode("hidden")}
                    aria-label="隐藏侧栏"
                  />
                </Tooltip>
              </>
            )}
          </div>
          <MainNav inlineCollapsed={siderCollapsed} />
        </Sider>
      )}

      <Layout className="app-shell-inner">
        {!isPlayerRoute && (
        <Header
          className="app-top-header app-shell-header"
          style={{
            padding: 0,
            background: "#0a0a0a",
            borderBottom: "1px solid #1f1f1f",
            height: 64,
            lineHeight: "normal",
          }}
        >
          <div
            className={`app-header-bar app-shell-header-bar${mode === "hidden" ? " app-shell-header-bar-hidden" : ""}`}
          >
            <div
              className={`app-header-left app-shell-header-left${mode === "hidden" ? " app-shell-header-left-hidden" : ""}`}
            >
              {mode === "hidden" ? (
                <span className="app-header-hidden-brand">
                  <Button
                    type="text"
                    className="app-hidden-nav-open-btn"
                    aria-label="打开导航"
                    icon={<MenuOutlined style={{ fontSize: 22, color: "#fff" }} />}
                    onClick={() => setDrawerOpen(true)}
                  />
                  {isHomeRoute ? (
                    <Link
                      to="/"
                      className="app-header-hidden-logo"
                      onClick={(e) => {
                        e.preventDefault();
                        nav("/");
                      }}
                    >
                      <span>Knox-Media</span>
                    </Link>
                  ) : pathTitle ? (
                    <Typography.Text className="app-shell-header-title" ellipsis>
                      {pathTitle}
                    </Typography.Text>
                  ) : null}
                </span>
              ) : null}
              {pathTitle && mode !== "hidden" ? (
                <Typography.Text className="app-shell-header-title" ellipsis>
                  {pathTitle}
                </Typography.Text>
              ) : null}
            </div>
            <div className="app-header-right app-shell-header-right">
              <Space size="middle">
                {admin && (
                  <Tooltip title="管理控制台">
                    <Button
                      type="text"
                      icon={<SettingOutlined style={{ fontSize: 20, color: "#00a4dc" }} />}
                      aria-label="管理控制台"
                      onClick={() => nav("/console")}
                    />
                  </Tooltip>
                )}
                <Dropdown
                  menu={{ items: userMenuItems, className: "app-user-dropdown-menu" }}
                  placement="bottomRight"
                  trigger={["click"]}
                >
                  <span className="app-shell-avatar-wrap" role="button" tabIndex={0}>
                    <Avatar
                      size="default"
                      src={avatarUrl || undefined}
                      style={{ backgroundColor: "#00a4dc", cursor: "pointer" }}
                    >
                      {avatarUrl ? null : (username || "?").slice(0, 1).toUpperCase()}
                    </Avatar>
                  </span>
                </Dropdown>
              </Space>
            </div>
          </div>
        </Header>
        )}

        {!isPlayerRoute && (
        <Drawer
          title={
            <span style={{ color: "#fff", display: "inline-flex", alignItems: "center", gap: 8 }}>
              <PlayCircleOutlined style={{ color: "#00a4dc" }} />
              Knox-Media
            </span>
          }
          extra={
            <Tooltip title="固定侧边栏">
              <Button
                type="text"
                icon={<PushpinOutlined style={{ color: "#ddd" }} />}
                aria-label="固定侧边栏"
                onClick={() => {
                  setAutoCollapseOnLeave(false);
                  setMode("expanded");
                  setDrawerOpen(false);
                }}
              />
            </Tooltip>
          }
          placement="left"
          width={280}
          onClose={() => setDrawerOpen(false)}
          open={drawerOpen}
          styles={{
            body: { padding: 0, background: "#141414" },
            header: { background: "#141414", borderBottom: "1px solid #222" },
          }}
          style={{ background: "#141414" }}
        >
          <div className="app-drawer-nav">
            <MainNav
              onNavigate={() => setDrawerOpen(false)}
              inlineCollapsed={false}
            />
          </div>
        </Drawer>
        )}

        <Content
          className={`app-shell-content${isPlayerRoute ? " app-shell-content-player" : ""}`}
          style={{
            background: "#000",
            minHeight: isPlayerRoute ? "100vh" : "calc(100vh - 64px)",
            overflow: isPlayerRoute ? "hidden" : "auto",
          }}
        >
          <div className={`app-main-centered${isPlayerRoute ? " app-main-centered-player" : ""}`}>
            <Outlet />
          </div>
        </Content>
      </Layout>
    </Layout>
  );
}

export default function App() {
  const token = useAuthStore((s) => s.token);

  return (
    <Routes>
      <Route
        path="/login"
        element={token ? <Navigate to="/" replace /> : <LoginPage />}
      />
      <Route element={<RequireAuth />}>
        <Route element={<MainShell />}>
          <Route index element={<HomePage />} />
          <Route path="favorites" element={<FavoritesPage />} />
          <Route path="browse" element={<BrowsePage />} />
          <Route path="series/:id" element={<SeriesDetailPage />} />
          <Route path="playback-history" element={<PlaybackHistoryPage />} />
          <Route path="search" element={<SearchPage />} />
          <Route path="detail/:id" element={<MediaDetailPage />} />
          <Route path="playlists" element={<PlaylistsPage />} />
          <Route path="media" element={<LegacyMediaToBrowse />} />
          <Route path="player/:id?" element={<PlayerPage />} />
          <Route path="settings" element={<SettingsPage />} />
          <Route element={<RequireAdmin />}>
            <Route path="library" element={<LibraryPage />} />
            <Route path="upload" element={<UploadPage />} />
            <Route path="media-manager" element={<MediaManagerPage />} />
            <Route path="tasks" element={<TaskManagerPage />} />
            <Route path="drm-license-audit" element={<DRMLicenseAuditPage />} />
            <Route path="access-logs" element={<AccessLogsPage />} />
            <Route path="api-credentials" element={<ApiCredentialsPage />} />
            <Route path="users" element={<UsersPage />} />
            <Route path="console" element={<AdminConsolePage />} />
            <Route path="system-options" element={<SystemOptionsPage />} />
            <Route path="scrape-config" element={<ScrapeConfigPage />} />
            <Route path="ai-provider" element={<AIProviderPage />} />
          </Route>
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Route>
    </Routes>
  );
}
