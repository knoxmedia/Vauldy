import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { MediaItem, photoThumbSrc } from "../api/client";
import { tGlobal } from "../i18n";
import styles from "./PhotoMasonry.module.css";

const MIN_COL_PX = 160;
const GAP_PX = 16;
const MAX_COLS = 7;
const CAPTION_PX = 22;
const CAPTION_GAP_PX = 6;

type Dim = { w: number; h: number };

type Props = {
  items: MediaItem[];
  onOpen: (id: number) => void;
};

function columnCount(containerWidth: number): number {
  if (containerWidth <= 0) return 1;
  const raw = Math.floor((containerWidth + GAP_PX) / (MIN_COL_PX + GAP_PX));
  return Math.max(1, Math.min(MAX_COLS, raw));
}

function columnWidthPx(containerWidth: number, cols: number): number {
  return (containerWidth - (cols - 1) * GAP_PX) / cols;
}

function resolveDim(item: MediaItem, dims: ReadonlyMap<number, Dim>): Dim {
  const loaded = dims.get(item.id);
  if (loaded && loaded.w > 0 && loaded.h > 0) return loaded;
  if (item.width > 0 && item.height > 0) return { w: item.width, h: item.height };
  return { w: 3, h: 4 };
}

function estimateCardHeight(item: MediaItem, colWidth: number, dims: ReadonlyMap<number, Dim>): number {
  const { w, h } = resolveDim(item, dims);
  const imageHeight = (colWidth * h) / w;
  return imageHeight + CAPTION_GAP_PX + CAPTION_PX;
}

/** 最短列优先：新卡片永远挂到当前累计高度最小的列 */
function distributeByShortestColumn(
  items: MediaItem[],
  cols: number,
  colWidth: number,
  dims: ReadonlyMap<number, Dim>,
): MediaItem[][] {
  const columns: MediaItem[][] = Array.from({ length: cols }, () => []);
  const heights = Array<number>(cols).fill(0);

  for (const item of items) {
    let target = 0;
    for (let i = 1; i < cols; i++) {
      if (heights[i] < heights[target]) target = i;
    }
    columns[target].push(item);
    heights[target] += estimateCardHeight(item, colWidth, dims) + GAP_PX;
  }

  return columns;
}

export default function PhotoMasonry({ items, onOpen }: Props) {
  const rootRef = useRef<HTMLDivElement>(null);
  const dimsRef = useRef<Map<number, Dim>>(new Map());
  const [containerWidth, setContainerWidth] = useState(0);
  const [dimVersion, setDimVersion] = useState(0);

  const bumpDims = useCallback(() => setDimVersion((v) => v + 1), []);

  const setDim = useCallback(
    (id: number, w: number, h: number) => {
      if (w <= 0 || h <= 0) return;
      const prev = dimsRef.current.get(id);
      if (prev && prev.w === w && prev.h === h) return;
      dimsRef.current.set(id, { w, h });
      bumpDims();
    },
    [bumpDims],
  );

  useLayoutEffect(() => {
    const el = rootRef.current;
    if (!el) return;

    const measure = () => {
      const w = el.getBoundingClientRect().width;
      if (w > 0) setContainerWidth(w);
    };

    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [items.length]);

  useEffect(() => {
    let changed = false;
    for (const item of items) {
      if (item.width > 0 && item.height > 0) {
        const prev = dimsRef.current.get(item.id);
        if (!prev || prev.w !== item.width || prev.h !== item.height) {
          dimsRef.current.set(item.id, { w: item.width, h: item.height });
          changed = true;
        }
      }
    }
    if (changed) bumpDims();
  }, [items, bumpDims]);

  useEffect(() => {
    let cancelled = false;
    const queue = items.filter((item) => {
      const d = dimsRef.current.get(item.id);
      return !d || d.w <= 0 || d.h <= 0;
    });
    if (queue.length === 0) return;

    let inFlight = 0;
    let index = 0;
    const maxConcurrent = 10;

    const pump = () => {
      while (!cancelled && inFlight < maxConcurrent && index < queue.length) {
        const item = queue[index++];
        inFlight++;
        const img = new Image();
        img.onload = () => {
          inFlight--;
          if (!cancelled && img.naturalWidth > 0 && img.naturalHeight > 0) {
            setDim(item.id, img.naturalWidth, img.naturalHeight);
          }
          pump();
        };
        img.onerror = () => {
          inFlight--;
          pump();
        };
        img.src = photoThumbSrc(item.id);
      }
    };

    pump();
    return () => {
      cancelled = true;
    };
  }, [items, setDim]);

  const cols = columnCount(containerWidth);
  const colWidth = containerWidth > 0 ? columnWidthPx(containerWidth, cols) : MIN_COL_PX;
  const dims = dimsRef.current;

  const columns = useMemo(
    () => distributeByShortestColumn(items, cols, colWidth, dims),
    [items, cols, colWidth, dimVersion],
  );

  const onImgLoad = useCallback(
    (id: number, img: HTMLImageElement) => {
      if (img.naturalWidth > 0 && img.naturalHeight > 0) {
        setDim(id, img.naturalWidth, img.naturalHeight);
      }
    },
    [setDim],
  );

  return (
    <div ref={rootRef} className={styles.root}>
      <div className={styles.columns} style={{ gap: GAP_PX }}>
        {columns.map((col, colIdx) => (
          <div
            key={colIdx}
            className={styles.column}
            style={{ width: colWidth, gap: GAP_PX }}
          >
            {col.map((item) => (
              <div
                key={item.id}
                className={styles.cell}
                onClick={() => onOpen(item.id)}
                title={item.title}
              >
                <div className={styles.item}>
                  <img
                    className={styles.img}
                    src={photoThumbSrc(item.id)}
                    alt={item.title || ""}
                    loading="lazy"
                    decoding="async"
                    onLoad={(e) => onImgLoad(item.id, e.currentTarget)}
                  />
                </div>
                <div className={styles.caption}>{item.title || tGlobal("components.photo_lightbox.untitled")}</div>
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}
