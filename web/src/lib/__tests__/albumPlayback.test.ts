import { beforeEach, describe, expect, it, vi } from "vitest";
import { ALBUM_PLAY_SESSION_KEY } from "../../api/client";
import {
  isAlbumPlaybackFinished,
  navigateAlbumNext,
  resolveNextAlbumMedia,
  resolvePrevAlbumMedia,
} from "../albumPlayback";

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

describe("albumPlayback", () => {
  beforeEach(() => {
    mockSessionStorage();
  });

  it("resolves the next track by index", () => {
    const session = { albumId: 3, order: [201, 202, 203] };
    expect(resolveNextAlbumMedia(session, 201, 0)).toEqual({ mediaId: 202, index: 1 });
  });

  it("resolves the previous track by index", () => {
    const session = { albumId: 3, order: [201, 202, 203] };
    expect(resolvePrevAlbumMedia(session, 202, 1)).toEqual({ mediaId: 201, index: 0 });
  });

  it("navigates to the next track when album session matches", () => {
    sessionStorage.setItem(
      ALBUM_PLAY_SESSION_KEY,
      JSON.stringify({ albumId: 3, order: [201, 202, 203] }),
    );
    const nav = vi.fn();
    const params = new URLSearchParams("album_id=3&index=0");
    expect(navigateAlbumNext(nav, params, 201)).toBe(true);
    expect(nav).toHaveBeenCalledWith("/player/202?album_id=3&index=1", { replace: true });
  });

  it("detects when album playback is finished", () => {
    sessionStorage.setItem(
      ALBUM_PLAY_SESSION_KEY,
      JSON.stringify({ albumId: 3, order: [201, 202] }),
    );
    const params = new URLSearchParams("album_id=3&index=1");
    expect(isAlbumPlaybackFinished(params, 202)).toBe(true);
  });
});
