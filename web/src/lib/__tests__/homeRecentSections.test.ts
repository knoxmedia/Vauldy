import { describe, expect, it, vi } from "vitest";
import type { Library, MediaItem } from "../../api/client";
import { homeRecentProjectionSignature, loadHomeRecentBySection, stableHomeRecentMap, type HomeRecentSection } from "../homeRecentSections";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

const lib = (id: number, type = "movie"): Library => ({ id, name: `L${id}`, type, path: "", auto_scan: 0, scraper: "", created_at: "" });
const media = (id: number, fields: Partial<MediaItem> = {}): MediaItem => ({ id, library_id: 1, file_id: `f${id}`, title: `M${id}`, file_path: `p${id}`, file_type: "video", duration: 0, width: 0, height: 0, format: "", status: "active", created_at: `2026-01-${String(id).padStart(2, "0")}`, ...fields });
const sections: HomeRecentSection[] = [
  { key: "movie", title: "Movies", libTypes: ["movie"], landscape: false },
  { key: "music", title: "Music", libTypes: ["music"], landscape: false },
];

describe("loadHomeRecentBySection", () => {
  it("loads sections and their libraries in parallel and commits each section independently", async () => {
    const pending = new Map<number, (items: MediaItem[]) => void>();
    const fetcher = vi.fn((id: number | undefined, _opts: unknown, signal?: AbortSignal) => new Promise<MediaItem[]>((resolve) => {
      expect(signal).toBe(controller.signal);
      pending.set(id!, resolve);
    }));
    const controller = new AbortController();
    const commits: string[] = [];
    const run = loadHomeRecentBySection([lib(1), lib(2), lib(3, "music")], sections, controller.signal, (key) => commits.push(key), fetcher);
    expect(fetcher).toHaveBeenCalledTimes(3);
    pending.get(3)!([media(30)]);
    await Promise.resolve();
    await Promise.resolve();
    expect(commits).toEqual(["music"]);
    pending.get(1)!([media(2), media(1)]);
    pending.get(2)!([media(2), media(3)]);
    await run;
    expect(commits).toEqual(["music", "movie"]);
  });

  it("does not commit a failed section or any callback after abort", async () => {
    const controller = new AbortController();
    const onSection = vi.fn();
    const fetcher = vi.fn((id: number | undefined) => id === 1 ? Promise.reject(new Error("boom")) : Promise.resolve([media(2)]));
    await loadHomeRecentBySection([lib(1), lib(2)], [sections[0]!], controller.signal, onSection, fetcher);
    expect(onSection).not.toHaveBeenCalled();

    const pendingAfterAbort = deferred<MediaItem[]>();
    const run = loadHomeRecentBySection([lib(3)], [sections[0]!], controller.signal, onSection, () => pendingAfterAbort.promise);
    controller.abort();
    pendingAfterAbort.resolve([media(3)]);
    await run;
    expect(onSection).not.toHaveBeenCalled();
  });

  it("deduplicates media stably after sorting", async () => {
    const controller = new AbortController();
    const onSection = vi.fn();
    await loadHomeRecentBySection([lib(1), lib(2)], [sections[0]!], controller.signal, onSection, (id) => Promise.resolve(id === 1 ? [media(1), media(2)] : [media(2), media(3)]));
    expect(onSection.mock.calls[0]![1].map((item: MediaItem) => item.id)).toEqual([3, 2, 1]);
  });
});

describe("stableHomeRecentMap", () => {
  it("preserves map and section references when display fields are unchanged", () => {
    const oldItems = [media(1, { poster_url: "/p" })];
    const previous = new Map([["movie", oldItems]]);
    const next = stableHomeRecentMap(previous, "movie", [media(1, { poster_url: "/p" })]);
    expect(next).toBe(previous);
    expect(next.get("movie")).toBe(oldItems);
  });

  it("replaces only the changed section", () => {
    const movies = [media(1)];
    const music = [media(2)];
    const previous = new Map([["movie", movies], ["music", music]]);
    const next = stableHomeRecentMap(previous, "movie", [media(1, { title: "changed" })]);
    expect(next).not.toBe(previous);
    expect(next.get("movie")).not.toBe(movies);
    expect(next.get("music")).toBe(music);
  });

  it("replaces a section when music artist or album display data changes", () => {
    const oldItems = [media(1, { music_artist: "Old Artist", music_album_title: "Old Album" })];
    const previous = new Map([["music", oldItems]]);
    const artistChanged = stableHomeRecentMap(previous, "music", [media(1, { music_artist: "New Artist", music_album_title: "Old Album" })]);
    expect(artistChanged).not.toBe(previous);
    const albumChanged = stableHomeRecentMap(previous, "music", [media(1, { music_artist: "Old Artist", music_album_title: "New Album" })]);
    expect(albumChanged).not.toBe(previous);
  });
  it.each([
    ["bitrate", { bitrate: 320_000 }],
    ["status", { status: "inactive" }],
    ["last_play_at", { last_play_at: "2026-07-18T00:00:00Z" }],
    ["scraped", { scraped: true }],
    ["library_type", { library_type: "music" }],
    ["photo_tags", { photo_tags: ["family"] }],
    ["photo_tag_ids", { photo_tag_ids: ["builtin:family"] }],
    ["optimization_asset_recorded", { optimization_asset_recorded: true }],
    ["optimization_available", { optimization_available: true }],
    ["optimization_source_available", { optimization_source_available: true }],
    ["encrypted_asset", { encrypted_asset: true }],
  ] satisfies Array<[string, Partial<MediaItem>]>)("replaces a section when operation field %s changes", (_field, changed) => {
    const previous = new Map([["movie", [media(1)]]]);
    expect(stableHomeRecentMap(previous, "movie", [media(1, changed)])).not.toBe(previous);
  });
  it("uses media id descending as a global timestamp tie-breaker independent of library order", async () => {
    const controller = new AbortController();
    const run = async (libraries: Library[]) => {
      const onSection = vi.fn();
      await loadHomeRecentBySection(libraries, [sections[0]!], controller.signal, onSection, (id) => Promise.resolve([
        media(id === 1 ? 10 : 20, { created_at: "2026-01-01" }),
      ]));
      return onSection.mock.calls[0]![1] as MediaItem[];
    };
    const first = await run([lib(1), lib(2)]);
    const second = await run([lib(2), lib(1)]);
    expect(first.map((item) => item.id)).toEqual([20, 10]);
    expect(second.map((item) => item.id)).toEqual([20, 10]);
    const previous = new Map([["movie", first]]);
    expect(stableHomeRecentMap(previous, "movie", second)).toBe(previous);
  });

  it("produces collision-safe projection signatures", () => {
    const left = media(1, { title: "a|b", original_title: "c=d", photo_tags: ["x|y", "z"] });
    const right = media(1, { title: "a", original_title: "b|c=d", photo_tags: ["x", "y|z"] });
    expect(homeRecentProjectionSignature(left)).not.toBe(homeRecentProjectionSignature(right));
  });});







\n