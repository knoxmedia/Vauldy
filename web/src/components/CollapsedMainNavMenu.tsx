import { Popover, Tooltip } from "antd";
import type { MenuProps } from "antd";
import { EllipsisOutlined } from "@ant-design/icons";
import { useLayoutEffect, useState, type RefObject } from "react";
import { useT } from "../i18n";

export type FlatNavItem = {
  key: string;
  icon?: React.ReactNode;
  title?: string;
  disabled?: boolean;
  label: React.ReactNode;
};

export function flattenNavMenuItems(items: MenuProps["items"]): FlatNavItem[] {
  const out: FlatNavItem[] = [];
  for (const raw of items ?? []) {
    if (!raw || raw.type === "divider" || raw.type === "group" || raw.type === "submenu") continue;
    if ("children" in raw && raw.children) continue;
    if (!("key" in raw) || raw.key == null) continue;
    const item = raw as {
      key?: React.Key;
      icon?: React.ReactNode;
      title?: React.ReactNode;
      disabled?: boolean;
      label?: React.ReactNode;
    };
    out.push({
      key: String(item.key),
      icon: item.icon,
      title: typeof item.title === "string" ? item.title : undefined,
      disabled: item.disabled,
      label: item.label,
    });
  }
  return out;
}

const ITEM_H = 40;
const MORE_H = 40;

function visibleItemCount(total: number, containerHeight: number): number {
  if (total <= 0 || containerHeight <= 0) return total;
  const maxAll = Math.floor(containerHeight / ITEM_H);
  if (total <= maxAll) return total;
  const withMore = Math.floor((containerHeight - MORE_H) / ITEM_H);
  return Math.max(1, Math.min(withMore, total - 1));
}

function CollapsedNavIcon({ item, selected }: { item: FlatNavItem; selected: boolean }) {
  const node = (
    <div
      className={`app-collapsed-nav-item${selected ? " app-collapsed-nav-item-selected" : ""}${
        item.disabled ? " app-collapsed-nav-item-disabled" : ""
      }`}
    >
      <span className="app-collapsed-nav-item-icon">{item.icon}</span>
      {!item.disabled ? <span className="app-collapsed-nav-item-label">{item.label}</span> : null}
    </div>
  );
  if (!item.title) return node;
  return (
    <Tooltip title={item.title} placement="right" mouseEnterDelay={0.15}>
      {node}
    </Tooltip>
  );
}

type CollapsedMainNavMenuProps = {
  items: FlatNavItem[];
  selectedKeys: string[];
  containerRef: RefObject<HTMLDivElement | null>;
};

export default function CollapsedMainNavMenu({
  items,
  selectedKeys,
  containerRef,
}: CollapsedMainNavMenuProps) {
  const t = useT();
  const [visibleCount, setVisibleCount] = useState(items.length);

  useLayoutEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    const calc = () => {
      setVisibleCount(visibleItemCount(items.length, el.clientHeight));
    };

    calc();
    const ro = new ResizeObserver(calc);
    ro.observe(el);
    return () => ro.disconnect();
  }, [containerRef, items.length]);

  const visible = items.slice(0, visibleCount);
  const overflow = items.slice(visibleCount);
  const overflowSelected = overflow.some((item) => selectedKeys.includes(item.key));

  const popoverContent = (
    <div className="app-collapsed-nav-popover-list">
      {overflow.map((item) => (
        <CollapsedNavIcon
          key={item.key}
          item={item}
          selected={selectedKeys.includes(item.key)}
        />
      ))}
    </div>
  );

  return (
    <div className="app-collapsed-nav">
      <div className="app-collapsed-nav-items">
        {visible.map((item) => (
          <CollapsedNavIcon
            key={item.key}
            item={item}
            selected={selectedKeys.includes(item.key)}
          />
        ))}
      </div>
      {overflow.length > 0 ? (
        <Popover
          content={popoverContent}
          trigger="hover"
          placement="rightBottom"
          overlayClassName="app-collapsed-nav-popover"
          arrow={false}
          mouseEnterDelay={0.12}
          mouseLeaveDelay={0.15}
        >
          <div
            className={`app-collapsed-nav-item app-collapsed-nav-more${
              overflowSelected ? " app-collapsed-nav-item-selected" : ""
            }`}
            aria-label={t("common.more")}
          >
            <span className="app-collapsed-nav-item-icon">
              <EllipsisOutlined />
            </span>
          </div>
        </Popover>
      ) : null}
    </div>
  );
}
