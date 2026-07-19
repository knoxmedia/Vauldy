import { describe, expect, it } from "vitest";
import type { MediaDetail, SeriesDetail } from "../../api/client";
import { mediaSearchRaw } from "../../components/MediaMatchModal";
import { buildSeriesMatchMedia, resolveSeriesMatchMedia } from "../seriesMatchTarget";

const series = {
  id: 7,
  library_id: 3,
  title: "\u53bb\u6709\u98ce\u7684\u5730\u65b9",
  year: 2023,
} satisfies SeriesDetail;

function episode(id: number, title: string): MediaDetail {
  return { id, title, file_path: `D:/TV/${title}.mkv` } as MediaDetail;
}

describe("buildSeriesMatchMedia", () => {
  it.each([
    ["first episode", episode(101, "Show.S01E01.2160p")],
    ["recently played episode", episode(108, "Show.S01E08.2160p")],
  ])("uses series metadata but keeps the %s id", (_label, representative) => {
    const target = buildSeriesMatchMedia(series, representative);

    expect(target).toEqual({
      id: representative.id,
      title: "\u53bb\u6709\u98ce\u7684\u5730\u65b9",
      year: 2023,
      file_path: "",
    });
    expect(mediaSearchRaw(target)).toBe("\u53bb\u6709\u98ce\u7684\u5730\u65b9");
  });

  it("uses the fallback media id without falling back to an episode filename", () => {
    const target = buildSeriesMatchMedia({ ...series, title: "   " }, undefined, 109);

    expect(target).toEqual({ id: 109, title: "   ", year: 2023, file_path: "" });
    expect(mediaSearchRaw(target)).toBe("");
  });
});


describe("resolveSeriesMatchMedia", () => {
  it("rejects detail and media state left over from the previous route", () => {
    expect(resolveSeriesMatchMedia(20, series, { seriesId: 10, media: episode(101, "Old.S01E01") }, { seriesId: 10, mediaId: 101 })).toBeNull();
  });

  it("does not use an old representative or fallback after the new detail loads", () => {
    const current = { ...series, id: 20, title: "New Series" };
    expect(resolveSeriesMatchMedia(20, current, { seriesId: 10, media: episode(101, "Old.S01E01") }, { seriesId: 10, mediaId: 101 })).toBeNull();
  });

  it("uses only media state tagged for the current series", () => {
    const current = { ...series, id: 20, title: "New Series" };
    expect(resolveSeriesMatchMedia(20, current, { seriesId: 20, media: episode(201, "New.S01E01") }, { seriesId: 20, mediaId: 202 })).toEqual({
      id: 201, title: "New Series", year: 2023, file_path: "",
    });
  });
});
