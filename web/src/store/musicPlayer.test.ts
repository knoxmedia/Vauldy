import { beforeEach, describe, expect, it, vi } from "vitest";
import { libraryTracksToQueue } from "../lib/albumPlayback";
import { useMusicPlayerStore } from "./musicPlayer";

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

describe("libraryTracksToQueue", () => {
  it("maps library track rows to queue items", () => {
    const queue = libraryTracksToQueue([
      {
        id: 1,
        media_id: 10,
        title: "Song A",
        artist: "Artist A",
        album_title: "Album A",
        album_id: 5,
        duration: 180,
      },
      { id: 2, media_id: 0, title: "Bad", album_id: 6 },
    ]);
    expect(queue).toHaveLength(1);
    expect(queue[0]).toMatchObject({
      mediaId: 10,
      title: "Song A",
      albumId: 5,
      duration: 180,
    });
  });
});

describe("musicPlayer onTrackEnded", () => {
  beforeEach(() => {
    mockSessionStorage();
    useMusicPlayerStore.getState().stop();
    useMusicPlayerStore.setState({ playMode: "sequential" });
  });

  const sampleQueue = [
    { mediaId: 1, title: "A", artist: "X", albumTitle: "Al", albumId: 1, duration: 10 },
    { mediaId: 2, title: "B", artist: "X", albumTitle: "Al", albumId: 1, duration: 20 },
    { mediaId: 3, title: "C", artist: "X", albumTitle: "Al", albumId: 1, duration: 30 },
  ];

  it("sequential advances to next track", () => {
    useMusicPlayerStore.getState().playQueue(sampleQueue, 0);
    useMusicPlayerStore.getState().onTrackEnded();
    const s = useMusicPlayerStore.getState();
    expect(s.queueIndex).toBe(1);
    expect(s.playing).toBe(true);
    expect(s.queue[s.queueIndex]?.mediaId).toBe(2);
  });

  it("sequential stops after last track", () => {
    useMusicPlayerStore.getState().playQueue(sampleQueue, 2);
    useMusicPlayerStore.getState().onTrackEnded();
    const s = useMusicPlayerStore.getState();
    expect(s.queueIndex).toBe(2);
    expect(s.playing).toBe(false);
  });

  it("repeat-one bumps replay token on track end", () => {
    useMusicPlayerStore.setState({ playMode: "repeat-one" });
    useMusicPlayerStore.getState().playQueue(sampleQueue, 1);
    const before = useMusicPlayerStore.getState().replayToken;
    useMusicPlayerStore.getState().onTrackEnded();
    const s = useMusicPlayerStore.getState();
    expect(s.queueIndex).toBe(1);
    expect(s.replayToken).toBe(before + 1);
    expect(s.playing).toBe(true);
  });

  it("repeat-one manual next advances to next track", () => {
    useMusicPlayerStore.setState({ playMode: "repeat-one" });
    useMusicPlayerStore.getState().playQueue(sampleQueue, 0);
    useMusicPlayerStore.getState().next();
    const s = useMusicPlayerStore.getState();
    expect(s.queueIndex).toBe(1);
    expect(s.queue[s.queueIndex]?.mediaId).toBe(2);
    expect(s.replayToken).toBe(0);
  });

  it("repeat-one manual prev advances to previous track", () => {
    useMusicPlayerStore.setState({ playMode: "repeat-one" });
    useMusicPlayerStore.getState().playQueue(sampleQueue, 1);
    useMusicPlayerStore.getState().prev();
    const s = useMusicPlayerStore.getState();
    expect(s.queueIndex).toBe(0);
    expect(s.queue[s.queueIndex]?.mediaId).toBe(1);
  });

  it("shuffle advances within queue", () => {
    useMusicPlayerStore.setState({ playMode: "shuffle", shuffleMap: [2, 0, 1] });
    useMusicPlayerStore.getState().playQueue(sampleQueue, 2);
    useMusicPlayerStore.getState().onTrackEnded();
    const s = useMusicPlayerStore.getState();
    expect(s.queueIndex).toBe(0);
    expect(s.playing).toBe(true);
  });

  it("loadAlbum sequential option overrides shuffle mode", () => {
    useMusicPlayerStore.setState({ playMode: "shuffle" });
    useMusicPlayerStore.getState().loadAlbum(1, sampleQueue, 0, { sequential: true });
    const s = useMusicPlayerStore.getState();
    expect(s.playMode).toBe("sequential");
    expect(s.shuffleMap).toBeNull();
    s.onTrackEnded();
    expect(useMusicPlayerStore.getState().queueIndex).toBe(1);
  });
});
