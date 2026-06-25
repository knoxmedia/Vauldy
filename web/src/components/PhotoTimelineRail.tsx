import { useEffect, useMemo, useState } from "react";
import type { TimelineMark } from "../lib/photoBrowseUtils";
import { useT } from "../i18n";
import styles from "./PhotoTimelineRail.module.css";

type Props = {
  marks: TimelineMark[];
};

function anchorId(mark: TimelineMark): string {
  return mark.kind === "year" ? `photo-year-${mark.year}` : `photo-month-${mark.key.slice(2)}`;
}

export default function PhotoTimelineRail({ marks }: Props) {
  const t = useT();
  const [activeKey, setActiveKey] = useState<string | null>(marks[0]?.key ?? null);

  const monthMarks = useMemo(() => marks.filter((m) => m.kind === "month"), [marks]);

  useEffect(() => {
    if (monthMarks.length === 0) return;

    const observer = new IntersectionObserver(
      (entries) => {
        const visible = entries
          .filter((e) => e.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
        if (visible.length === 0) return;
        const id = visible[0].target.id;
        if (id.startsWith("photo-month-")) {
          setActiveKey(`m-${id.replace("photo-month-", "")}`);
        }
      },
      { rootMargin: "-20% 0px -70% 0px", threshold: 0 },
    );

    for (const mark of monthMarks) {
      const el = document.getElementById(anchorId(mark));
      if (el) observer.observe(el);
    }
    return () => observer.disconnect();
  }, [monthMarks]);

  if (marks.length === 0) return null;

  function jump(mark: TimelineMark) {
    const el = document.getElementById(anchorId(mark));
    if (el) {
      el.scrollIntoView({ behavior: "smooth", block: "start" });
      setActiveKey(mark.key);
    }
  }

  return (
    <aside className={styles.rail} aria-label={t("components.photo_timeline_rail.aria_nav")}>
      <div className={styles.track}>
        {marks.map((mark) => (
          <button
            key={mark.key}
            type="button"
            className={`${styles.mark} ${mark.kind === "year" ? styles.yearMark : styles.monthMark} ${
              activeKey === mark.key ? styles.markActive : ""
            }`}
            onClick={() => jump(mark)}
          >
            {mark.kind === "year" ? mark.label : mark.label.replace(/^\d{4}年/, "")}
            <span className={styles.tooltip}>{mark.label}</span>
          </button>
        ))}
      </div>
    </aside>
  );
}
