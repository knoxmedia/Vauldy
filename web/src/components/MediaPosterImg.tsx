import { useMemo, useState } from "react";
import {
  MediaItem,
  hasScrapedPosterUrl,
  localPosterSrc,
  mediaPosterSrc,
} from "../api/client";

type Props = {
  item: Pick<MediaItem, "id" | "poster_url">;
  className?: string;
  onLoad?: (e: React.SyntheticEvent<HTMLImageElement>) => void;
  onLoadStart?: (e: React.SyntheticEvent<HTMLImageElement>) => void;
};

/** Grid/list poster: prefer scraped image, fall back to local frame capture on load error. */
export default function MediaPosterImg({ item, className, onLoad, onLoadStart }: Props) {
  const [scrapedFailed, setScrapedFailed] = useState(false);
  const src = useMemo(() => {
    if (scrapedFailed && hasScrapedPosterUrl(item)) return localPosterSrc(item.id);
    return mediaPosterSrc(item);
  }, [item, scrapedFailed]);

  return (
    <img
      className={className}
      src={src}
      alt=""
      loading="lazy"
      decoding="async"
      onLoadStart={onLoadStart}
      onLoad={onLoad}
      onError={(e) => {
        if (!scrapedFailed && hasScrapedPosterUrl(item)) {
          setScrapedFailed(true);
          return;
        }
        e.currentTarget.style.display = "none";
        e.currentTarget.parentElement?.removeAttribute("data-cover-loaded");
      }}
    />
  );
}
