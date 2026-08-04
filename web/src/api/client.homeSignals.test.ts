import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("antd", () => ({ message: { error: vi.fn() } }));
vi.mock("../store/auth", () => ({ useAuthStore: Object.assign(() => undefined, { getState: () => ({ token: null, clearSession: vi.fn() }) }) }));
vi.mock("../lib/imageUrl", () => ({ proxyImageSrc: (value: string) => value }));

import { api, fetchLibraries, fetchMedia, fetchUserHistory } from "./client";

afterEach(() => vi.restoreAllMocks());

describe("home API abort signals", () => {
  it("passes optional AbortSignal without changing legacy argument positions", async () => {
    const signal = new AbortController().signal;
    const get = vi.spyOn(api, "get")
      .mockResolvedValueOnce({ data: { items: [] } })
      .mockResolvedValueOnce({ data: { items: [] } })
      .mockResolvedValueOnce({ data: { items: [] } });

    await fetchLibraries(signal);
    await fetchMedia(7, { limit: 24 }, signal);
    await fetchUserHistory(24, { libraryTypes: ["movie"] }, signal);

    expect(get.mock.calls[0]![1]).toMatchObject({ signal });
    expect(get.mock.calls[1]![1]).toMatchObject({ signal, params: { library_id: 7, limit: 24 } });
    expect(get.mock.calls[2]![1]).toMatchObject({ signal, params: { limit: 24, library_types: "movie" } });
  });
});
