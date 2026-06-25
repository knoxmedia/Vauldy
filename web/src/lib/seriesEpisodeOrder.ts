import type { EpisodeRow, SeasonSummary } from "../api/client";
import { fetchSeasonEpisodes } from "../api/client";

/** Primary file for an episode (lowest sort_order version). */
export function pickPrimaryEpisodeMediaId(ep: EpisodeRow): number | null {
  const versions = ep.versions ?? [];
  if (versions.length === 0) return null;
  const sorted = [...versions].sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0));
  const mid = sorted[0]?.media_id;
  return mid != null && mid > 0 ? mid : null;
}

/** All episode media IDs in season/episode order (primary version per episode). */
export async function fetchSeriesEpisodeMediaOrder(seasons: SeasonSummary[]): Promise<number[]> {
  const order: number[] = [];
  const sortedSeasons = [...seasons].sort((a, b) => a.season_num - b.season_num);
  for (const season of sortedSeasons) {
    let items: EpisodeRow[] = [];
    try {
      items = await fetchSeasonEpisodes(season.id);
    } catch {
      continue;
    }
    const sortedEpisodes = [...items].sort((a, b) => a.episode_num - b.episode_num);
    for (const ep of sortedEpisodes) {
      const mid = pickPrimaryEpisodeMediaId(ep);
      if (mid != null) order.push(mid);
    }
  }
  return order;
}
