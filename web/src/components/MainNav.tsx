import { Button, Input, Menu, Spin } from "antd";
import type { MenuProps } from "antd";
import {
  AppstoreOutlined,
  ApiOutlined,
  DatabaseOutlined,
  RobotOutlined,
  CloudUploadOutlined,
  ControlOutlined,
  EditOutlined,
  FolderOpenOutlined,
  HomeOutlined,
  HistoryOutlined,
  LoadingOutlined,
  TeamOutlined,
  ScheduleOutlined,
  SearchOutlined,
  SettingOutlined,
  StarOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { useEffect, useMemo, useRef, useState } from "react";
import { fetchLibrariesWithCapabilities, type Library } from "../api/client";
import { libraryTypeIcon } from "../lib/libraryTypeIcon";
import { isAdminRole, useAuthStore } from "../store/auth";
import { useT } from "../i18n";
import CollapsedMainNavMenu, { flattenNavMenuItems } from "./CollapsedMainNavMenu";

type MainNavProps = {
  /** 关闭抽屉（侧栏隐藏模式下的浮动菜单） */
  onNavigate?: () => void;
  /** 侧栏折叠为仅图标时，子菜单用弹出层 */
  inlineCollapsed?: boolean;
};

export default function MainNav({ onNavigate, inlineCollapsed }: MainNavProps) {
  const navigate = useNavigate();
  const loc = useLocation();
  const t = useT();
  const path = loc.pathname;
  const search = loc.search;
  const role = useAuthStore((s) => s.role);
  const admin = isAdminRole(role);

  const [libs, setLibs] = useState<Library[]>([]);
  const [libsLoading, setLibsLoading] = useState(true);
  const [widevineEnabled, setWidevineEnabled] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setLibsLoading(true);
    void fetchLibrariesWithCapabilities()
      .then(({ items, drmCapabilities }) => {
        if (!cancelled) {
          setLibs(Array.isArray(items) ? items : []);
          setWidevineEnabled(!!drmCapabilities.widevine_enabled);
        }
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
    if (path.startsWith("/playback-history")) return ["playback-history"];
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
    if (path.startsWith("/system-options")) return ["system-options"];
    if (path.startsWith("/scrape-config")) return ["scrape-config"];
    if (path.startsWith("/ai-provider")) return ["ai-provider"];
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
        path.startsWith("/console") ||
        path.startsWith("/scrape-config") ||
        path.startsWith("/ai-provider") ||
        path.startsWith("/system-options"))
    ) {
      keys.push("admin-section");
    }
    return keys;
  }, [path, selectedKeys, admin]);

  const libraryChildren: NonNullable<MenuProps["items"]> = useMemo(() => {
    if (libsLoading && libs.length === 0) {
      return [
        {
          key: "lib-loading",
          disabled: true,
          icon: <LoadingOutlined />,
          title: t("common.loading"),
          label: (
            <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
              <Spin size="small" /> {t("common.loading")}
            </span>
          ),
        },
      ];
    }
    if (libs.length === 0) {
      return [
        {
          key: "lib-empty",
          disabled: true,
          icon: <FolderOpenOutlined />,
          title: t("nav.no_library"),
          label: <span style={{ color: "#666" }}>{t("nav.no_library")}</span>,
        },
      ];
    }
    return libs.map((lib) => ({
      key: `lib-${lib.id}`,
      icon: libraryTypeIcon(lib.type),
      title: lib.name,
      label: (
        <Link to={`/browse?library_id=${lib.id}`} onClick={onNavigate}>
          {lib.name}
        </Link>
      ),
    }));
  }, [libs, libsLoading, onNavigate, t]);

  const adminChildren: NonNullable<MenuProps["items"]> = useMemo(
    () => {
      const items: NonNullable<MenuProps["items"]> = [
      {
        key: "console",
        icon: <ControlOutlined />,
        title: t("nav.console"),
        label: (
          <Link to="/console" onClick={onNavigate}>
            {t("nav.console")}
          </Link>
        ),
      },
      {
        key: "system-options",
        icon: <SettingOutlined />,
        title: t("nav.system_options"),
        label: (
          <Link to="/system-options" onClick={onNavigate}>
            {t("nav.system_options")}
          </Link>
        ),
      },
      {
        key: "library",
        icon: <FolderOpenOutlined />,
        title: t("nav.library"),
        label: (
          <Link to="/library" onClick={onNavigate}>
            {t("nav.library")}
          </Link>
        ),
      },
      {
        key: "upload",
        icon: <CloudUploadOutlined />,
        title: t("nav.upload"),
        label: (
          <Link to="/upload" onClick={onNavigate}>
            {t("nav.upload")}
          </Link>
        ),
      },
      {
        key: "media-manager",
        icon: <EditOutlined />,
        title: t("nav.media_manager"),
        label: (
          <Link to="/media-manager" onClick={onNavigate}>
            {t("nav.media_manager")}
          </Link>
        ),
      },
      {
        key: "tasks",
        icon: <ScheduleOutlined />,
        title: t("nav.tasks"),
        label: (
          <Link to="/tasks" onClick={onNavigate}>
            {t("nav.tasks")}
          </Link>
        ),
      },
      ];
      if (widevineEnabled) {
        items.push({
          key: "drm-license-audit",
          icon: <HistoryOutlined />,
          title: t("nav.drm_audit"),
          label: (
            <Link to="/drm-license-audit" onClick={onNavigate}>
              {t("nav.drm_audit")}
            </Link>
          ),
        });
      }
      items.push(
      {
        key: "access-logs",
        icon: <HistoryOutlined />,
        title: t("nav.access_logs"),
        label: (
          <Link to="/access-logs" onClick={onNavigate}>
            {t("nav.access_logs")}
          </Link>
        ),
      },
      {
        key: "users",
        icon: <TeamOutlined />,
        title: t("nav.users"),
        label: (
          <Link to="/users" onClick={onNavigate}>
            {t("nav.users")}
          </Link>
        ),
      },
      {
        key: "api-credentials",
        icon: <ApiOutlined />,
        title: t("nav.api_credentials"),
        label: (
          <Link to="/api-credentials" onClick={onNavigate}>
            {t("nav.api_credentials")}
          </Link>
        ),
      },
      {
        key: "scrape-config",
        icon: <DatabaseOutlined />,
        title: t("nav.scrape_config"),
        label: (
          <Link to="/scrape-config" onClick={onNavigate}>
            {t("nav.scrape_config")}
          </Link>
        ),
      },
      {
        key: "ai-provider",
        icon: <RobotOutlined />,
        title: t("nav.ai_provider"),
        label: (
          <Link to="/ai-provider" onClick={onNavigate}>
            {t("nav.ai_provider")}
          </Link>
        ),
      },
      );
      return items;
    },
    [onNavigate, t, widevineEnabled],
  );

  const menuItems: MenuProps["items"] = useMemo(() => {
    const topLevelItems: MenuProps["items"] = [
      {
        key: "home",
        icon: <HomeOutlined />,
        title: t("nav.home"),
        label: (
          <Link to="/" onClick={onNavigate}>
            {t("nav.home")}
          </Link>
        ),
      },
      {
        key: "favorites",
        icon: <StarOutlined />,
        title: t("nav.favorites"),
        label: (
          <Link to="/favorites" onClick={onNavigate}>
            {t("nav.favorites")}
          </Link>
        ),
      },
    ];

    if (inlineCollapsed) {
      topLevelItems.push(...libraryChildren);
    } else {
      topLevelItems.push({
        key: "my-media",
        icon: <AppstoreOutlined />,
        label: t("nav.my_media"),
        children: [...libraryChildren],
      });
    }

    topLevelItems.push(
      {
        key: "playlists",
        icon: <UnorderedListOutlined />,
        title: t("nav.playlists"),
        label: (
          <Link to="/playlists" onClick={onNavigate}>
            {t("nav.playlists")}
          </Link>
        ),
      },
      {
        key: "playback-history",
        icon: <HistoryOutlined />,
        title: t("nav.playback_history"),
        label: (
          <Link to="/playback-history" onClick={onNavigate}>
            {t("nav.playback_history")}
          </Link>
        ),
      },
    );

    if (admin) {
      topLevelItems.push({ type: "divider" });
      if (inlineCollapsed) {
        topLevelItems.push(...adminChildren);
      } else {
        topLevelItems.push({
          key: "admin-section",
          icon: <ControlOutlined />,
          label: t("nav.management"),
          children: [...adminChildren],
        });
      }
    }

    return topLevelItems;
  }, [admin, adminChildren, inlineCollapsed, libraryChildren, onNavigate, t]);

  const [openKeys, setOpenKeys] = useState<string[]>(["my-media"]);

  useEffect(() => {
    if (openKeysDefault.length) {
      setOpenKeys((prev) => Array.from(new Set([...prev, ...openKeysDefault])));
    }
  }, [openKeysDefault]);

  const navBodyRef = useRef<HTMLDivElement>(null);
  const flatCollapsedItems = useMemo(
    () => (inlineCollapsed ? flattenNavMenuItems(menuItems) : []),
    [inlineCollapsed, menuItems],
  );

  return (
    <div className={`app-main-nav${inlineCollapsed ? "" : " app-main-nav-expanded"}`}>
      <div className="app-main-nav-search">
        {inlineCollapsed ? (
          <Button
            type="text"
            className="app-main-nav-search-icon"
            aria-label={t("nav.search_aria")}
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
            placeholder={t("nav.search_placeholder")}
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
      <div ref={navBodyRef} className="app-main-nav-body">
        {inlineCollapsed ? (
          <CollapsedMainNavMenu
            items={flatCollapsedItems}
            selectedKeys={selectedKeys}
            containerRef={navBodyRef}
          />
        ) : (
          <Menu
            theme="dark"
            mode="inline"
            inlineCollapsed={false}
            className="app-main-nav-menu"
            style={{ border: 0, background: "transparent", flex: 1 }}
            selectedKeys={selectedKeys}
            openKeys={openKeys}
            onOpenChange={setOpenKeys}
            items={menuItems}
          />
        )}
      </div>
    </div>
  );
}
