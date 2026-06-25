import { VerticalAlignTopOutlined } from "@ant-design/icons";
import { Button, Tooltip } from "antd";
import { useCallback, useEffect, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useT } from "../i18n";
import styles from "./ScrollToTopFab.module.css";

const SHOW_AFTER_PX = 200;

type Props = {
  hidden?: boolean;
  bottomOffset?: number;
  scrollRootRef?: RefObject<HTMLElement | null>;
};

function readScrollTop(root: HTMLElement | null | undefined): number {
  const elTop = root?.scrollTop ?? 0;
  const docTop = document.documentElement.scrollTop || document.body.scrollTop || 0;
  const winTop = window.scrollY || 0;
  return Math.max(elTop, docTop, winTop);
}

export default function ScrollToTopFab({ hidden = false, bottomOffset = 24, scrollRootRef }: Props) {
  const t = useT();
  const visible = useVisibleScroll(!hidden, scrollRootRef);

  const scrollToTop = useCallback(() => {
    const root = scrollRootRef?.current;
    root?.scrollTo({ top: 0, behavior: "smooth" });
    window.scrollTo({ top: 0, behavior: "smooth" });
    document.documentElement.scrollTo({ top: 0, behavior: "smooth" });
    document.body.scrollTo?.({ top: 0, behavior: "smooth" });
  }, [scrollRootRef]);

  if (hidden || !visible) return null;

  return createPortal(
    <Tooltip title={t("components.scroll_to_top.back_to_top")} placement="left">
      <Button
        type="default"
        shape="circle"
        className={styles.fab}
        style={{ bottom: bottomOffset }}
        icon={<VerticalAlignTopOutlined />}
        aria-label={t("components.scroll_to_top.back_to_top")}
        onClick={scrollToTop}
      />
    </Tooltip>,
    document.body,
  );
}

function useVisibleScroll(enabled: boolean, scrollRootRef?: RefObject<HTMLElement | null>) {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    if (!enabled) {
      setVisible(false);
      return;
    }

    const update = () => {
      setVisible(readScrollTop(scrollRootRef?.current) > SHOW_AFTER_PX);
    };

    update();
    document.addEventListener("scroll", update, { passive: true, capture: true });
    window.addEventListener("resize", update, { passive: true });
    return () => {
      document.removeEventListener("scroll", update, { capture: true });
      window.removeEventListener("resize", update);
    };
  }, [enabled, scrollRootRef]);

  return visible;
}
