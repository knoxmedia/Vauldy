import { Button, Input, Menu, Spin } from "antd";
import type { MenuProps } from "antd";
import {
  AppstoreOutlined,
  ApiOutlined,
  CloudUploadOutlined,
  ControlOutlined,
  EditOutlined,
  FolderOpenOutlined,
  HomeOutlined,
  HistoryOutlined,
  TeamOutlined,
  ScheduleOutlined,
  SearchOutlined,
  StarOutlined,
  UnorderedListOutlined,
  VideoCameraOutlined,
} from "@ant-design/icons";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { useEffect, useMemo, useState } from "react";
import { fetchLibraries, type Library } from "../api/client";
import { isAdminRole, useAuthStore } from "../store/auth";

function libIcon(type: string) {
  if (type === "movie" || type === "tv" || type === "anime") {
    return <VideoCameraOutlined />;
  }
  return <FolderOpenOutlined />;
}

type MainNavProps = {
  /** 关闭抽屉（侧栏隐藏模式下的浮动菜单） */
  onNavigate?: () => void;
  /** 侧栏折叠为仅图标时，子菜单用弹出层 */
  inlineCollapsed?: boolean;
};

export default function MainNav({ onNavigate, inlineCollapsed }: MainNavProps) {
  const navigate = useNavigate();
  const loc = useLocation();
  const path = loc.pathname;
  const search = loc.search;
  const role = useAuthStore((s) => s.role);
  const admin = isAdminRole(role);

  const [libs, setLibs] = useState<Library[]>([]);
  const [libsLoading, setLibsLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setLibsLoading(true);
    void fetchLibraries()
      .then((items) => {
        if (!cancelled) setLibs(Array.isArray(items) ? items : []);
      })
      .catch(() => {
        if (!cancelled) setLibs([]);
      })
      .finally(() => {
        if (!cancelled) setLibsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const selectedKeys = useMemo(() => {
    if (path === "/" || path === "") return ["home"];
    if (path.startsWith("/favorites")) return ["favorites"];
    if (path.startsWith("/playlists")) return ["playlists"];
    if (path.startsWith("/browse")) {
      const p = new URLSearchParams(search);
      const lid = p.get("library_id");
      if (lid && !Number.isNaN(Number(lid))) return [`lib-${lid}`];
      return ["my-media"];
    }
    if (path.startsWith("/player")) return ["player"];
    if (path.startsWith("/settings")) return ["settings"];
    if (path.startsWith("/library")) return ["library"];
    if (path.startsWith("/upload")) return ["upload"];
    if (path.startsWith("/media-manager")) return ["media-manager"];
    if (path.startsWith("/tasks")) return ["tasks"];
    if (path.startsWith("/drm-license-audit")) return ["drm-license-audit"];
    if (path.startsWith("/access-logs")) return ["access-logs"];
    if (path.startsWith("/api-credentials")) return ["api-credentials"];
    if (path.startsWith("/users")) return ["users"];
    if (path.startsWith("/console")) return ["console"];
    return [];
  }, [path, search]);

  const openKeysDefault = useMemo(() => {
    const keys: string[] = [];
    if (path.startsWith("/browse")) {
      keys.push("my-media");
    }
    if (
      admin &&
      (path.startsWith("/library") ||
        path.startsWith("/upload") ||
        path.startsWith("/media-manager") ||
        path.startsWith("/tasks") ||
        path.startsWith("/drm-license-audit") ||
        path.startsWith("/access-logs") ||
        path.startsWith("/api-credentials") ||
        path.startsWith("/users") ||
        path.startsWith("/console"))
    ) {
      keys.push("admin-section");
    }
    return keys;
  }, [path, selectedKeys, admin]);

  const [openKeys, setOpenKeys] = useState<string[]>(["my-media"]);

  const firstLibrary = libs.length > 0 ? libs[0] : undefined;

  useEffect(() => {
    if (openKeysDefault.length) {
      setOpenKeys((prev) => Array.from(new Set([...prev, ...openKeysDefault])));
    }
  }, [openKeysDefault]);

  const menuItems: MenuProps["items"] = useMemo(() => {
    const libraryChildren: NonNullable<MenuProps["items"]> =
      libsLoading && libs.length === 0
        ? [
            {
              key: "lib-loading",
              disabled: true,
              label: (
                <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
                  <Spin size="small" /> 加载中…
                </span>
              ),
            },
          ]
        : libs.length === 0
          ? [
              {
                key: "lib-empty",
                disabled: true,
                label: <span style={{ color: "#666" }}>暂无媒体库</span>,
              },
            ]
          : libs.map((lib) => ({
              key: `lib-${lib.id}`,
              icon: libIcon(lib.type),
              label: (
                <Link to={`/browse?library_id=${lib.id}`} onClick={onNavigate}>
                  {lib.name}
                </Link>
              ),
            }));

    const items: MenuProps["items"] = [
      {
        key: "home",
        icon: <HomeOutlined />,
        label: (
          <Link to="/" onClick={onNavigate}>
            首页
          </Link>
        ),
      },
      {
        key: "favorites",
        icon: <StarOutlined />,
        label: (
          <Link to="/favorites" onClick={onNavigate}>
            我的收藏
          </Link>
        ),
      },
      ...(inlineCollapsed
        ? [
            {
              key: "my-media",
              icon: firstLibrary ? libIcon(firstLibrary.type) : <FolderOpenOutlined />,
              label: (
                <Link
                  to={firstLibrary ? `/browse?library_id=${firstLibrary.id}` : "/browse"}
                  onClick={onNavigate}
                >
                  我的媒体
                </Link>
              ),
            },
          ]
        : [
            {
              key: "my-media",
              icon: <AppstoreOutlined />,
              label: "我的媒体",
              children: [...libraryChildren],
            },
          ]),
      {
        key: "playlists",
        icon: <UnorderedListOutlined />,
        label: (
          <Link to="/playlists" onClick={onNavigate}>
            播放列表
          </Link>
        ),
      },
    ];

    if (admin) {
      items.push({
        type: "divider",
      });
      items.push({
        key: "admin-section",
        icon: <ControlOutlined />,
        label: (
          <Link to="/console" onClick={onNavigate}>
            管理
          </Link>
        ),
        children: [
          {
            key: "console",
            icon: <ControlOutlined />,
            label: (
              <Link to="/console" onClick={onNavigate}>
                控制台
              </Link>
            ),
          },
          {
            key: "library",
            icon: <FolderOpenOutlined />,
            label: (
              <Link to="/library" onClick={onNavigate}>
                媒体库
              </Link>
            ),
          },
          {
            key: "upload",
            icon: <CloudUploadOutlined />,
            label: (
              <Link to="/upload" onClick={onNavigate}>
                上传
              </Link>
            ),
          },
          {
            key: "media-manager",
            icon: <EditOutlined />,
            label: (
              <Link to="/media-manager" onClick={onNavigate}>
                媒体资料管理
              </Link>
            ),
          },
          {
            key: "tasks",
            icon: <ScheduleOutlined />,
            label: (
              <Link to="/tasks" onClick={onNavigate}>
                任务管理
              </Link>
            ),
          },
          {
            key: "drm-license-audit",
            icon: <HistoryOutlined />,
            label: (
              <Link to="/drm-license-audit" onClick={onNavigate}>
                DRM许可证审计
              </Link>
            ),
          },
          {
            key: "access-logs",
            icon: <HistoryOutlined />,
            label: (
              <Link to="/access-logs" onClick={onNavigate}>
                访问日志
              </Link>
            ),
          },
          {
            key: "users",
            icon: <TeamOutlined />,
            label: (
              <Link to="/users" onClick={onNavigate}>
                用户管理
              </Link>
            ),
          },
          {
            key: "api-credentials",
            icon: <ApiOutlined />,
            label: (
              <Link to="/api-credentials" onClick={onNavigate}>
                API 凭证
              </Link>
            ),
          },
        ],
      });
    }

    return items;
  }, [libs, libsLoading, admin, onNavigate]);

  return (
    <div className="app-main-nav">
      <div className="app-main-nav-search">
        {inlineCollapsed ? (
          <Button
            type="text"
            className="app-main-nav-search-icon"
            aria-label="搜索"
            icon={<SearchOutlined style={{ color: "#ddd", fontSize: 18 }} />}
            onClick={() => {
              onNavigate?.();
              navigate("/search");
            }}
          />
        ) : (
          <Input
            allowClear
            prefix={<SearchOutlined style={{ color: "#666" }} />}
            placeholder="搜索"
            className="app-main-nav-search-input"
            onPressEnter={(e) => {
              const el = e.target as HTMLInputElement;
              const v = el.value.trim();
              onNavigate?.();
              navigate(v ? `/search?q=${encodeURIComponent(v)}` : "/search");
            }}
          />
        )}
      </div>
      <Menu
        theme="dark"
        mode="inline"
        inlineCollapsed={inlineCollapsed}
        style={{ border: 0, background: "transparent", flex: 1 }}
        selectedKeys={selectedKeys}
        openKeys={inlineCollapsed ? [] : openKeys}
        onOpenChange={setOpenKeys}
        items={menuItems}
      />
    </div>
  );
}
