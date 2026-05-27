import type { MediaItem, PhotoCategory, PhotoPlace } from "../api/client";
import { photoThumbSrc } from "../api/client";
import { categoriesForSection, sampleCover, type DrillDown } from "../lib/photoBrowseUtils";
import styles from "./PhotoSmartClassify.module.css";

type Props = {
  categories: PhotoCategory[];
  places: PhotoPlace[];
  items: MediaItem[];
  onOpen: (drill: DrillDown) => void;
};

function CategorySectionRow({
  title,
  categories,
  items,
  section,
  onOpen,
  showAll = false,
}: {
  title: string;
  categories: PhotoCategory[];
  items: MediaItem[];
  section: DrillDown["section"];
  onOpen: (drill: DrillDown) => void;
  showAll?: boolean;
}) {
  const visible = showAll ? categories : categories.slice(0, 8);
  const rest = showAll ? 0 : categories.length - visible.length;

  if (categories.length === 0) {
    return (
      <section className={styles.section}>
        <h3 className={styles.sectionTitle}>{title}</h3>
        <p className={styles.emptyHint}>暂无分类结果，请先完成 AI 智能分类。</p>
      </section>
    );
  }

  return (
    <section className={styles.section}>
      <h3 className={styles.sectionTitle}>{title}</h3>
      <div className={showAll ? `${styles.row} ${styles.rowWrap}` : styles.row}>
        {visible.map((cat) => {
          const coverId = sampleCover(items, cat.id);
          return (
            <button
              key={cat.id}
              type="button"
              className={styles.tile}
              onClick={() => onOpen({ section, categoryId: cat.id, title: cat.name })}
            >
              <div className={styles.cover}>
                {coverId != null ? (
                  <img src={photoThumbSrc(coverId)} alt="" loading="lazy" decoding="async" />
                ) : (
                  <div className={styles.placeholder}>图</div>
                )}
              </div>
              <div className={styles.tileLabel}>{cat.name}</div>
              <div className={styles.tileCount}>{cat.count} 张</div>
            </button>
          );
        })}
        {rest > 0 && visible[0] ? (
          <button
            type="button"
            className={`${styles.tile} ${styles.moreTile}`}
            onClick={() => onOpen({ section, categoryId: visible[0].id, title: visible[0].name })}
          >
            <div className={styles.cover}>
              {(() => {
                const coverId = sampleCover(items, visible[0].id);
                return coverId != null ? (
                  <img src={photoThumbSrc(coverId)} alt="" loading="lazy" decoding="async" />
                ) : (
                  <div className={styles.placeholder}>图</div>
                );
              })()}
              <span className={styles.moreLabel}>查看更多</span>
            </div>
          </button>
        ) : null}
      </div>
    </section>
  );
}

function PlaceSectionRow({
  places,
  onOpen,
}: {
  places: PhotoPlace[];
  onOpen: (drill: DrillDown) => void;
}) {
  const visible = places.slice(0, 8);
  const rest = places.length - visible.length;

  if (places.length === 0) {
    return (
      <section className={styles.section}>
        <h3 className={styles.sectionTitle}>地点</h3>
        <p className={styles.emptyHint}>
          暂无地点数据。请确认照片 EXIF 含 GPS 信息，并由管理员执行「解析 GPS 地点」或重新扫描图片库。
        </p>
      </section>
    );
  }

  return (
    <section className={styles.section}>
      <h3 className={styles.sectionTitle}>地点</h3>
      <div className={styles.row}>
        {visible.map((place) => (
          <button
            key={place.id}
            type="button"
            className={styles.tile}
            onClick={() => onOpen({ section: "place", categoryId: place.id, title: place.name || place.id })}
          >
            <div className={styles.cover}>
              {place.cover_id ? (
                <img src={photoThumbSrc(place.cover_id)} alt="" loading="lazy" decoding="async" />
              ) : (
                <div className={styles.placeholder}>图</div>
              )}
            </div>
            <div className={styles.tileLabel}>{place.name || "未知地点"}</div>
            <div className={styles.tileCount}>{place.count} 张</div>
          </button>
        ))}
        {rest > 0 && visible[0] ? (
          <button
            type="button"
            className={`${styles.tile} ${styles.moreTile}`}
            onClick={() =>
              onOpen({ section: "place", categoryId: visible[0].id, title: visible[0].name || visible[0].id })
            }
          >
            <div className={styles.cover}>
              {visible[0].cover_id ? (
                <img src={photoThumbSrc(visible[0].cover_id)} alt="" loading="lazy" decoding="async" />
              ) : (
                <div className={styles.placeholder}>图</div>
              )}
              <span className={styles.moreLabel}>查看更多</span>
            </div>
          </button>
        ) : null}
      </div>
    </section>
  );
}

export default function PhotoSmartClassify({ categories, places, items, onOpen }: Props) {
  return (
    <div>
      <CategorySectionRow
        title="人物"
        categories={categoriesForSection(categories, "people")}
        items={items}
        section="people"
        onOpen={onOpen}
      />
      <PlaceSectionRow places={places} onOpen={onOpen} />
      <CategorySectionRow
        title="事物"
        categories={categoriesForSection(categories, "thing")}
        items={items}
        section="thing"
        onOpen={onOpen}
        showAll
      />
    </div>
  );
}
