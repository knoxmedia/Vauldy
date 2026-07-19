import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("antd", () => ({ message: { error: vi.fn() } }));
const authState = vi.hoisted(() => ({ token: null as string | null, clearSession: vi.fn() }));
vi.mock("../store/auth", () => ({ useAuthStore: Object.assign(() => undefined, { getState: () => authState }) }));
vi.mock("../lib/imageUrl", () => ({ proxyImageSrc: (value: string) => value }));

import {
  api, fetchDocumentFacets, fetchDocumentNodes, fetchDocuments, fetchLibraryAlbums,
  fetchLibraryArtists, fetchLibraryGenres, fetchLibrarySeries, fetchLibraryTracks,
  fetchPhotoCategories, fetchPhotoClassifyProgress, fetchPhotoFaceProgress,
  fetchPhotoLocationProgress, fetchPhotoPersons, fetchPhotoPlaces, fetchRecentDocuments,
  reportPlaybackEnd, reportPlaybackEndKeepalive, reportPlaybackStart, savePlaybackProgress,
  savePlaybackProgressKeepalive,
} from "./client";

afterEach(() => vi.restoreAllMocks());

describe("specialized browse API abort signals", () => {
  it("passes the same signal to all specialized initial read APIs", async () => {
    const signal = new AbortController().signal;
    const get = vi.spyOn(api, "get").mockResolvedValue({ data: { items: [] } });
    await fetchLibrarySeries(7, signal);
    await fetchLibraryAlbums(7, signal); await fetchLibraryArtists(7, signal);
    await fetchLibraryGenres(7, signal); await fetchLibraryTracks(7, signal);
    await fetchPhotoCategories(7, signal); await fetchPhotoPlaces(7, signal); await fetchPhotoPersons(7, signal);
    await fetchPhotoFaceProgress(7, signal); await fetchPhotoLocationProgress(7, signal); await fetchPhotoClassifyProgress(7, signal);
    await fetchDocuments(7, { sort: "title" }, signal); await fetchDocumentNodes(7, "", signal);
    await fetchDocumentFacets(7, "author", signal); await fetchRecentDocuments(7, signal);
    expect(get).toHaveBeenCalledTimes(15);
    for (const call of get.mock.calls) expect(call[1]).toMatchObject({ signal });
  });
});

describe("playback evidence API", () => {
  it("passes typed evidence and JIT IDs without rewriting session_id", async () => {
    const post = vi.spyOn(api, "post").mockResolvedValue({ data: {} });
    const evidence = {
      position: 17,
      event: "start" as const,
      session_id: "app-session",
      sequence: 1,
      jit_session_id: "jit-session",
    };

    await reportPlaybackStart(42, evidence);
    await reportPlaybackEnd(42, { ...evidence, event: "ended", sequence: 2 });

    expect(post).toHaveBeenNthCalledWith(1, "/api/v1/media/42/playback/start", evidence);
    expect(post).toHaveBeenNthCalledWith(2, "/api/v1/media/42/playback/end", {
      ...evidence, event: "ended", sequence: 2,
    });
  });

  it("returns the backend playback progress decision", async () => {
    const result = {
      completed: true,
      auto_completed: true,
      effective_position: 94,
      stale: false,
    };
    const payload = {
      position: 94,
      event: "progress" as const,
      session_id: "app-session",
      sequence: 3,
      jit_session_id: "jit-session",
    };
    const post = vi.spyOn(api, "post").mockResolvedValue({ data: result });

    await expect(savePlaybackProgress(42, payload)).resolves.toEqual(result);
    expect(post).toHaveBeenCalledWith("/api/v1/media/42/progress", payload);
  });

  it("keeps legacy playback payload calls compatible", async () => {
    const post = vi.spyOn(api, "post").mockResolvedValue({
      data: { completed: false, auto_completed: false, effective_position: 5, stale: false },
    });

    await reportPlaybackStart(42, { position: 5, completed: 0, session_id: "legacy-jit" });
    await savePlaybackProgress(42, { position: 5, completed: 0, session_id: "legacy-jit" });

    expect(post).toHaveBeenNthCalledWith(1, "/api/v1/media/42/playback/start", {
      position: 5, completed: 0, session_id: "legacy-jit",
    });
    expect(post).toHaveBeenNthCalledWith(2, "/api/v1/media/42/progress", {
      position: 5, completed: 0, session_id: "legacy-jit",
    });
  });

  it("sends authenticated playback shutdown with fetch keepalive", async () => {
    authState.token = "secret-token";
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(reportPlaybackEndKeepalive(42, { position: 17, jit_session_id: "jit-a" })).resolves.toBe(true);

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/media/42/playback/end", {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: "Bearer secret-token" },
      body: JSON.stringify({ position: 17, jit_session_id: "jit-a" }),
      credentials: "same-origin",
      keepalive: true,
    });
  });

  it("refuses oversized keepalive payloads before fetch", async () => {
    authState.token = "secret-token";
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(reportPlaybackEndKeepalive(42, { position: 1, jit_session_id: "x".repeat(17 * 1024) })).resolves.toBe(false);
    expect(fetchMock).not.toHaveBeenCalled();
  });


  it("sends complete authenticated playback evidence with fetch keepalive", async () => {
    authState.token = "secret-token";
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    const payload = { position: 55, event: "progress" as const, session_id: "app-session", sequence: 7, jit_session_id: "jit-a" };

    await expect(savePlaybackProgressKeepalive(42, payload)).resolves.toBe(true);

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/media/42/progress", {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: "Bearer secret-token" },
      body: JSON.stringify(payload),
      credentials: "same-origin",
      keepalive: true,
    });
    expect(String(fetchMock.mock.calls[0]![0])).not.toContain("access_token");
  });

  it("refuses oversized progress keepalive payloads before fetch", async () => {
    authState.token = "secret-token";
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(savePlaybackProgressKeepalive(42, {
      position: 1, event: "progress", session_id: "x".repeat(17 * 1024), sequence: 2,
    })).resolves.toBe(false);
    expect(fetchMock).not.toHaveBeenCalled();
  });

});
