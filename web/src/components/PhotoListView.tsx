import type { MediaItem } from "../api/client";
import { photoThumbSrc } from "../api/client";
import type { LayoutMode, MonthBucket } from "../lib/photoBrowseUtils";
import { tGlobal } from "../i18n";
import PhotoMasonry from "./PhotoMasonry";
import styles from "./PhotoListView.module.css";

type Props = {
  items: MediaItem[];
  layout: LayoutMode;
  months?: MonthBucket[];
  onOpen: (id: number) => void;
  showDateGroups?: boolean;
};

function GridCard({ item, onOpen }: { item: MediaItem; onOpen: (id: number) => void }) {
  return (
    <div className={styles.card} onClick={() => onOpen(item.id)} title={item.title}>
      <img src={photoThumbSrc(item.id)} alt={item.title || ""} loading="lazy" decoding="async" />
      <div className={styles.cardTitle}>{item.title || tGlobal("components.photo_lightbox.untitled")}</div>
    </div>
  );
}

export default function PhotoListView({ items, layout, months, onOpen, showDateGroups = true }: Props) {
  if (showDateGroups && months && months.length > 0) {
    return (
      <div>
        {months.map((month, monthIdx) => {
          const prevYear = monthIdx > 0 ? months[monthIdx - 1].year : -1;
          const showYearAnchor = month.year > 0 && month.year !== prevYear;
          return (
            <section key={month.key} className={styles.monthGroup}>
              <div id={`photo-month-${month.key}`} className={styles.monthAnchor} aria-hidden />
              {showYearAnchor ? (
                <div id={`photo-year-${month.year}`} className={styles.monthAnchor} aria-hidden />
              ) : null}
              {month.days.map(({ day, label, items: dayItems }) => (
                <div key={day} className={styles.dateGroup}>
                  <h3 className={styles.dateHeading}>
                    {tGlobal("components.photo_list_view.date_with_count", { label, count: dayItems.length })}
                  </h3>
                  {layout === "masonry" ? (
                    <PhotoMasonry items={dayItems} onOpen={onOpen} />
                  ) : (
                    <div className={styles.grid}>
                      {dayItems.map((item) => (
                        <GridCard key={item.id} item={item} onOpen={onOpen} />
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </section>
          );
        })}
      </div>
    );
  }

  if (layout === "masonry") {
    return <PhotoMasonry items={items} onOpen={onOpen} />;
  }

  return (
    <div className={styles.grid}>
      {items.map((item) => (
        <GridCard key={item.id} item={item} onOpen={onOpen} />
      ))}
    </div>
  );
}
