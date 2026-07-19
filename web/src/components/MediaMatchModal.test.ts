import { describe, expect, it } from "vitest";
import type { ScrapeMatchCandidate } from "../api/client";
import { filterMatchCandidates } from "./MediaMatchModal";

const candidates: ScrapeMatchCandidate[] = [
  { source: "tmdb", external_id: "movie-1", media_type: "movie", title: "Same Name" },
  { source: "tmdb", external_id: "tv-1", media_type: "tv", title: "Same Name" },
  { source: "tvdb", external_id: "series-1", media_type: "series", title: "Same Name" },
  { source: "legacy-tv", external_id: "show-1", media_type: "show", title: "Same Name" },
  { source: "douban", external_id: "unknown-1", title: "Same Name" },
  { source: "omdb", external_id: "unknown-2", title: "Same Name" },
];

describe("filterMatchCandidates", () => {
  it("keeps only explicitly series-shaped candidates in series mode", () => {
    expect(filterMatchCandidates(candidates, "series").map((item) => item.external_id)).toEqual([
      "tv-1",
      "series-1",
      "show-1",
    ]);
  });

  it("keeps movie and TV candidates for the default modal", () => {
    expect(filterMatchCandidates(candidates)).toEqual(candidates);
  });
});
