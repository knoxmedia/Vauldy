import { describe, expect, it } from "vitest";
import { optimizationAssetRecorded, type MediaItem } from "./client";

const item = (fields: Partial<MediaItem>): MediaItem => ({
  id: 1, library_id: 1, file_id: "f", title: "t", file_path: "p", file_type: "video",
  duration: 0, width: 0, height: 0, format: "", status: "active", ...fields,
});

describe("optimizationAssetRecorded", () => {
  it("prefers the new field while preserving false", () => {
    expect(optimizationAssetRecorded(item({ optimization_asset_recorded: false, optimization_available: true }))).toBe(false);
  });

  it("falls back to the old-server alias", () => {
    expect(optimizationAssetRecorded(item({ optimization_available: true }))).toBe(true);
  });

  it("is conservative when neither field exists", () => {
    expect(optimizationAssetRecorded(item({}))).toBe(false);
  });
});

describe("MediaItem zero values", () => {
  it("preserves completed zero", () => {
    const media = item({ completed: 0 });
    expect(media.completed ?? 1).toBe(0);
  });
});
