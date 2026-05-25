import { beforeEach, describe, expect, it, vi } from "vitest";
import { SERIES_PLAY_SESSION_KEY } from "../../api/client";
import {
  buildSeriesPageUrl,
  isSeriesPlaybackFinished,
  navigateSeriesNext,
  resolveNextSeriesMedia,
} from "../seriesPlayback";

function mockSessionStorage() {
  const store = new Map<string, string>();
  vi.stubGlobal("sessionStorage", {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => {
      store.set(key, value);
    },
    removeItem: (key: string) => {
      store.delete(key);
    },
    clear: () => {
      store.clear();
    },
  });
}

describe("seriesPlayback", () => {
  beforeEach(() => {
    mockSessionStorage();
  });

  it("resolves the next episode by index", () => {
    const session = { seriesId: 7, order: [101, 102, 103] };
    expect(resolveNextSeriesMedia(session, 101, 0)).toEqual({ mediaId: 102, index: 1 });
  });

  it("navigates to the next episode when series session matches", () => {
    sessionStorage.setItem(
      SERIES_PLAY_SESSION_KEY,
      JSON.stringify({ seriesId: 7, order: [101, 102, 103] })
    );
    const nav = vi.fn();
    const params = new URLSearchParams("series_id=7&index=0");
    expect(navigateSeriesNext(nav, params, 101)).toBe(true);
    expect(nav).toHaveBeenCalledWith("/player/102?series_id=7&index=1", { replace: true });
  });

  it("detects when the current episode is the last in the session", () => {
    sessionStorage.setItem(
      SERIES_PLAY_SESSION_KEY,
      JSON.stringify({ seriesId: 7, order: [101, 102] })
    );
    const params = new URLSearchParams("series_id=7&index=1");
    expect(isSeriesPlaybackFinished(params, 102)).toBe(true);
    expect(isSeriesPlaybackFinished(params, 101)).toBe(false);
  });

  it("builds series page url with current media highlight", () => {
    expect(buildSeriesPageUrl(7, 101)).toBe("/series/7?current_media_id=101");
    expect(buildSeriesPageUrl(7)).toBe("/series/7");
  });
});
