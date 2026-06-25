import { describe, expect, it, vi, beforeEach } from "vitest";
import { PLAYLIST_PLAY_SESSION_KEY } from "../../api/client";
import {
  buildPlaylistPageUrl,
  isPlaylistPlaybackFinished,
  navigatePlaylistNext,
  readPlaylistPlaySession,
  resolveNextPlaylistMedia,
} from "../playlistPlayback";

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

describe("playlistPlayback", () => {
  beforeEach(() => {
    mockSessionStorage();
  });
  it("reads session from sessionStorage", () => {
    sessionStorage.setItem(
      PLAYLIST_PLAY_SESSION_KEY,
      JSON.stringify({ playlistId: 9, order: [101, 102, 103], mode: "shuffle" })
    );
    expect(readPlaylistPlaySession()).toEqual({
      playlistId: 9,
      order: [101, 102, 103],
      mode: "shuffle",
    });
  });

  it("resolves next media by URL index when it matches current media", () => {
    const session = { playlistId: 1, order: [10, 20, 30] };
    expect(resolveNextPlaylistMedia(session, 20, 1)).toEqual({ mediaId: 30, index: 2 });
  });

  it("falls back to order index when URL index is missing or stale", () => {
    const session = { playlistId: 1, order: [10, 20, 30] };
    expect(resolveNextPlaylistMedia(session, 20, null)).toEqual({ mediaId: 30, index: 2 });
    expect(resolveNextPlaylistMedia(session, 20, 99)).toEqual({ mediaId: 30, index: 2 });
  });

  it("returns null at end of playlist", () => {
    const session = { playlistId: 1, order: [10, 20] };
    expect(resolveNextPlaylistMedia(session, 20, 1)).toBeNull();
  });

  it("navigates to next item without full reload when playlist session matches", () => {
    sessionStorage.setItem(
      PLAYLIST_PLAY_SESSION_KEY,
      JSON.stringify({ playlistId: 5, order: [11, 22, 33], mode: "ordered" })
    );
    const nav = vi.fn();
    const params = new URLSearchParams("playlist_id=5&index=0");
    expect(navigatePlaylistNext(nav, params, 11)).toBe(true);
    expect(nav).toHaveBeenCalledWith("/player/22?playlist_id=5&index=1", { replace: true });
  });

  it("does not navigate when playlist_id query does not match session", () => {
    sessionStorage.setItem(
      PLAYLIST_PLAY_SESSION_KEY,
      JSON.stringify({ playlistId: 5, order: [11, 22], mode: "ordered" })
    );
    const nav = vi.fn();
    const params = new URLSearchParams("playlist_id=99&index=0");
    expect(navigatePlaylistNext(nav, params, 11)).toBe(false);
    expect(nav).not.toHaveBeenCalled();
  });

  it("detects when the current item is the last in the playlist session", () => {
    sessionStorage.setItem(
      PLAYLIST_PLAY_SESSION_KEY,
      JSON.stringify({ playlistId: 5, order: [11, 22, 33], mode: "ordered" })
    );
    const params = new URLSearchParams("playlist_id=5&index=2");
    expect(isPlaylistPlaybackFinished(params, 33)).toBe(true);
    expect(isPlaylistPlaybackFinished(params, 22)).toBe(false);
  });

  it("builds playlist page url with current media highlight", () => {
    expect(buildPlaylistPageUrl(5, 101)).toBe("/playlists?playlist_id=5&current_media_id=101");
    expect(buildPlaylistPageUrl(5)).toBe("/playlists?playlist_id=5");
  });
});
