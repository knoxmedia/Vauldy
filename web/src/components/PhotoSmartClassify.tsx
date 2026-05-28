import { useEffect, useRef, useState, type RefObject } from "react";
import type { MediaItem, PhotoCategory, PhotoPerson, PhotoPlace } from "../api/client";
import { photoFaceThumbSrc, photoThumbSrc } from "../api/client";
import { categoriesForSection, PERSON_ALL_ID, PLACE_ALL_ID, sampleCover, type DrillDown } from "../lib/photoBrowseUtils";
import { tGlobal as t } from "../i18n";
import styles from "./PhotoSmartClassify.module.css";

const TILE_WIDTH = 120;
const TILE_GAP = 12;

type Props = {
  categories: PhotoCategory[];
  places: PhotoPlace[];
  persons: PhotoPerson[];
  items: MediaItem[];
  onOpen: (drill: DrillDown) => void;
};

function useVisibleTileCount(containerRef: RefObject<HTMLElement | null>) {
  const [count, setCount] = useState(8);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    const calc = () => {
      const width = el.clientWidth;
      const next = Math.max(1, Math.floor((width + TILE_GAP) / (TILE_WIDTH + TILE_GAP)));
      setCount(next);
    };

    calc();
    const ro = new ResizeObserver(calc);
    ro.observe(el);
    return () => ro.disconnect();
  }, [containerRef]);

  return count;
}

function PersonMoreTile({
  coverFaceId,
  onClick,
}: {
  coverFaceId?: number;
  onClick: () => void;
}) {
  return (
    <button type="button" className={`${styles.tile} ${styles.moreTile}`} onClick={onClick}>
      <div className={`${styles.cover} ${styles.faceCover}`}>
        {coverFaceId ? (
          <img src={photoFaceThumbSrc(coverFaceId)} alt="" loading="lazy" decoding="async" />
        ) : (
          <div className={styles.placeholder}>{t("components.photo_smart_classify.person_placeholder")}</div>
        )}
        <span className={styles.moreLabel}>{t("components.photo_smart_classify.view_more")}</span>
      </div>
      <div className={`${styles.tileLabel} ${styles.tileMetaHidden}`} aria-hidden="true">
        {t("components.photo_smart_classify.unnamed_person")}
      </div>
      <div className={`${styles.tileCount} ${styles.tileMetaHidden}`} aria-hidden="true">
        {t("components.photo_smart_classify.count_photos", { count: 0 })}
      </div>
    </button>
  );
}

function PersonTile({
  person,
  onOpen,
}: {
  person: PhotoPerson;
  onOpen: (drill: DrillDown) => void;
}) {
  return (
    <button
      type="button"
      className={styles.tile}
      onClick={() =>
        onOpen({
          section: "person",
          categoryId: String(person.id),
          title: person.name || t("components.photo_smart_classify.person_fallback", { id: person.id }),
        })
      }
    >
      <div className={`${styles.cover} ${styles.faceCover}`}>
        {person.cover_face_id ? (
          <img src={photoFaceThumbSrc(person.cover_face_id)} alt="" loading="lazy" decoding="async" />
        ) : (
          <div className={styles.placeholder}>{t("components.photo_smart_classify.person_placeholder")}</div>
        )}
      </div>
      <div className={styles.tileLabel}>{person.name || t("components.photo_smart_classify.unnamed_person")}</div>
      <div className={styles.tileCount}>{t("components.photo_smart_classify.count_photos", { count: person.count })}</div>
    </button>
  );
}

function CategorySectionRow({
  title,
  categories,
  items,
  section,
  onOpen,
  showAll = false,
  emptyHint,
}: {
  title: string;
  categories: PhotoCategory[];
  items: MediaItem[];
  section: "people" | "thing";
  onOpen: (drill: DrillDown) => void;
  showAll?: boolean;
  emptyHint?: string;
}) {
  const visible = showAll ? categories : categories.slice(0, 8);
  const rest = showAll ? 0 : categories.length - visible.length;

  const emptyText = emptyHint ?? t("components.photo_smart_classify.empty_hint_default");

  if (categories.length === 0) {
    return (
      <section className={styles.section}>
        <h3 className={styles.sectionTitle}>{title}</h3>
        <p className={styles.emptyHint}>{emptyText}</p>
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
                  <div className={styles.placeholder}>{t("components.photo_smart_classify.img_placeholder")}</div>
                )}
              </div>
              <div className={styles.tileLabel}>{cat.name}</div>
              <div className={styles.tileCount}>{t("components.photo_smart_classify.count_photos", { count: cat.count })}</div>
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
                  <div className={styles.placeholder}>{t("components.photo_smart_classify.img_placeholder")}</div>
                );
              })()}
              <span className={styles.moreLabel}>{t("components.photo_smart_classify.view_more")}</span>
            </div>
            <div className={`${styles.tileLabel} ${styles.tileMetaHidden}`} aria-hidden="true">
              {t("components.photo_smart_classify.placeholder_label")}
            </div>
            <div className={`${styles.tileCount} ${styles.tileMetaHidden}`} aria-hidden="true">
              {t("components.photo_smart_classify.count_photos", { count: 0 })}
            </div>
          </button>
        ) : null}
      </div>
    </section>
  );
}

function PersonSectionRow({
  persons,
  onOpen,
}: {
  persons: PhotoPerson[];
  onOpen: (drill: DrillDown) => void;
}) {
  const rowRef = useRef<HTMLDivElement>(null);
  const capacity = useVisibleTileCount(rowRef);
  const hasMore = persons.length > capacity;
  const visibleCount = hasMore ? Math.max(0, capacity - 1) : Math.min(persons.length, capacity);
  const visible = persons.slice(0, visibleCount);
  const coverPerson = hasMore ? persons[visibleCount] ?? persons[0] : null;

  if (persons.length === 0) {
    return (
      <section className={styles.section}>
        <h3 className={styles.sectionTitle}>{t("components.photo_smart_classify.section_people")}</h3>
        <p className={styles.emptyHint}>
          {t("components.photo_smart_classify.no_people_hint")}
        </p>
      </section>
    );
  }

  return (
    <section className={styles.section}>
      <h3 className={styles.sectionTitle}>{t("components.photo_smart_classify.section_people")}</h3>
      <div ref={rowRef} className={`${styles.row} ${styles.rowFit}`}>
        {visible.map((person) => (
          <PersonTile key={person.id} person={person} onOpen={onOpen} />
        ))}
        {hasMore && coverPerson ? (
          <PersonMoreTile
            coverFaceId={coverPerson.cover_face_id}
            onClick={() => onOpen({ section: "person", categoryId: PERSON_ALL_ID, title: t("components.photo_smart_classify.section_people") })}
          />
        ) : null}
      </div>
    </section>
  );
}

export function PhotoPersonAllGrid({
  persons,
  onOpen,
}: {
  persons: PhotoPerson[];
  onOpen: (drill: DrillDown) => void;
}) {
  if (persons.length === 0) {
    return (
      <p className={styles.emptyHint}>
        {t("components.photo_smart_classify.no_people_hint")}
      </p>
    );
  }

  return (
    <div className={`${styles.row} ${styles.rowWrap}`}>
      {persons.map((person) => (
        <PersonTile key={person.id} person={person} onOpen={onOpen} />
      ))}
    </div>
  );
}

function PlaceMoreTile({
  coverId,
  onClick,
}: {
  coverId?: number;
  onClick: () => void;
}) {
  return (
    <button type="button" className={`${styles.tile} ${styles.moreTile}`} onClick={onClick}>
      <div className={styles.cover}>
        {coverId ? (
          <img src={photoThumbSrc(coverId)} alt="" loading="lazy" decoding="async" />
        ) : (
          <div className={styles.placeholder}>{t("components.photo_smart_classify.img_placeholder")}</div>
        )}
        <span className={styles.moreLabel}>{t("components.photo_smart_classify.view_more")}</span>
      </div>
      <div className={`${styles.tileLabel} ${styles.tileMetaHidden}`} aria-hidden="true">
        {t("components.photo_smart_classify.unknown_place")}
      </div>
      <div className={`${styles.tileCount} ${styles.tileMetaHidden}`} aria-hidden="true">
        {t("components.photo_smart_classify.count_photos", { count: 0 })}
      </div>
    </button>
  );
}

function PlaceTile({
  place,
  onOpen,
}: {
  place: PhotoPlace;
  onOpen: (drill: DrillDown) => void;
}) {
  return (
    <button
      type="button"
      className={styles.tile}
      onClick={() => onOpen({ section: "place", categoryId: place.id, title: place.name || place.id })}
    >
      <div className={styles.cover}>
        {place.cover_id ? (
          <img src={photoThumbSrc(place.cover_id)} alt="" loading="lazy" decoding="async" />
        ) : (
          <div className={styles.placeholder}>{t("components.photo_smart_classify.img_placeholder")}</div>
        )}
      </div>
      <div className={styles.tileLabel}>{place.name || t("components.photo_smart_classify.unknown_place")}</div>
      <div className={styles.tileCount}>{t("components.photo_smart_classify.count_photos", { count: place.count })}</div>
    </button>
  );
}

function PlaceSectionRow({
  places,
  onOpen,
}: {
  places: PhotoPlace[];
  onOpen: (drill: DrillDown) => void;
}) {
  const rowRef = useRef<HTMLDivElement>(null);
  const capacity = useVisibleTileCount(rowRef);
  const hasMore = places.length > capacity;
  const visibleCount = hasMore ? Math.max(0, capacity - 1) : Math.min(places.length, capacity);
  const visible = places.slice(0, visibleCount);
  const coverPlace = hasMore ? places[visibleCount] ?? places[0] : null;

  if (places.length === 0) {
    return (
      <section className={styles.section}>
        <h3 className={styles.sectionTitle}>{t("components.photo_smart_classify.section_places")}</h3>
        <p className={styles.emptyHint}>
          {t("components.photo_smart_classify.no_places_hint")}
        </p>
      </section>
    );
  }

  return (
    <section className={styles.section}>
      <h3 className={styles.sectionTitle}>{t("components.photo_smart_classify.section_places")}</h3>
      <div ref={rowRef} className={`${styles.row} ${styles.rowFit}`}>
        {visible.map((place) => (
          <PlaceTile key={place.id} place={place} onOpen={onOpen} />
        ))}
        {hasMore && coverPlace ? (
          <PlaceMoreTile
            coverId={coverPlace.cover_id}
            onClick={() => onOpen({ section: "place", categoryId: PLACE_ALL_ID, title: t("components.photo_smart_classify.section_places") })}
          />
        ) : null}
      </div>
    </section>
  );
}

export function PhotoPlaceAllGrid({
  places,
  onOpen,
}: {
  places: PhotoPlace[];
  onOpen: (drill: DrillDown) => void;
}) {
  if (places.length === 0) {
    return (
      <p className={styles.emptyHint}>
        {t("components.photo_smart_classify.no_places_hint")}
      </p>
    );
  }

  return (
    <div className={`${styles.row} ${styles.rowWrap}`}>
      {places.map((place) => (
        <PlaceTile key={place.id} place={place} onOpen={onOpen} />
      ))}
    </div>
  );
}

export default function PhotoSmartClassify({ categories, places, persons, items, onOpen }: Props) {
  return (
    <div>
      <PersonSectionRow persons={persons} onOpen={onOpen} />
      <PlaceSectionRow places={places} onOpen={onOpen} />
      <CategorySectionRow
        title={t("components.photo_smart_classify.section_things")}
        categories={categoriesForSection(categories, "thing")}
        items={items}
        section="thing"
        onOpen={onOpen}
        showAll
      />
    </div>
  );
}
