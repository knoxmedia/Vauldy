import { create } from "zustand";
import {
  clearAlbumPlaySession,
  storeQueueSession,
  type MusicQueueItem,
} from "../lib/albumPlayback";

export type MusicPlayMode = "sequential" | "repeat-all" | "repeat-one" | "shuffle";

const VOLUME_KEY = "knox.music.volume.v1";
const MODE_KEY = "knox.music.playMode.v1";

function readVolume(): number {
  try {
    const v = Number(localStorage.getItem(VOLUME_KEY));
    if (Number.isFinite(v) && v >= 0 && v <= 1) return v;
  } catch {
    /* ignore */
  }
  return 0.85;
}

function readPlayMode(): MusicPlayMode {
  try {
    const v = localStorage.getItem(MODE_KEY);
    if (v === "sequential" || v === "repeat-all" || v === "repeat-one" || v === "shuffle") return v;
  } catch {
    /* ignore */
  }
  return "sequential";
}

function shuffleOrder(length: number, avoidIndex: number): number[] {
  const idx = Array.from({ length }, (_, i) => i);
  for (let i = idx.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [idx[i], idx[j]] = [idx[j]!, idx[i]!];
  }
  if (length > 1 && idx[0] === avoidIndex) {
    [idx[0], idx[1]] = [idx[1]!, idx[0]!];
  }
  return idx;
}

type MusicPlayerState = {
  active: boolean;
  queue: MusicQueueItem[];
  queueIndex: number;
  albumId: number | null;
  playing: boolean;
  position: number;
  duration: number;
  volume: number;
  muted: boolean;
  playMode: MusicPlayMode;
  shuffleMap: number[] | null;
  replayToken: number;
  /** When true, MusicPlayerBar shows MusicFullscreenPlayer. */
  fullscreen: boolean;
  loadAlbum: (albumId: number, queue: MusicQueueItem[], startIndex?: number, options?: { sequential?: boolean }) => boolean;
  playTrack: (item: MusicQueueItem, queue?: MusicQueueItem[], index?: number) => boolean;
  playQueue: (queue: MusicQueueItem[], startIndex?: number) => boolean;
  play: () => void;
  pause: () => void;
  toggle: () => void;
  next: () => void;
  prev: () => void;
  seek: (sec: number) => void;
  setVolume: (v: number) => void;
  toggleMute: () => void;
  cyclePlayMode: () => void;
  onTrackEnded: () => void;
  syncPosition: (pos: number, dur: number) => void;
  stop: () => void;
  openFullscreen: () => void;
  closeFullscreen: () => void;
  /** Called by MusicPlayerBar when audio element is ready to play. */
  setPlaying: (playing: boolean) => void;
};

export const useMusicPlayerStore = create<MusicPlayerState>((set, get) => ({
  active: false,
  queue: [],
  queueIndex: 0,
  albumId: null,
  playing: false,
  position: 0,
  duration: 0,
  volume: readVolume(),
  muted: false,
  playMode: readPlayMode(),
  shuffleMap: null,
  replayToken: 0,
  fullscreen: false,

  openFullscreen: () => set({ fullscreen: true }),
  closeFullscreen: () => set({ fullscreen: false }),

  loadAlbum: (albumId, queue, startIndex = 0, options) => {
    const filtered = queue.filter((q) => q.mediaId > 0);
    if (filtered.length === 0) return false;
    const idx = Math.max(0, Math.min(startIndex, filtered.length - 1));
    storeQueueSession(filtered);
    const playMode = options?.sequential ? "sequential" : get().playMode;
    set({
      active: true,
      albumId,
      queue: filtered,
      queueIndex: idx,
      playing: true,
      position: 0,
      duration: filtered[idx]?.duration ?? 0,
      playMode,
      shuffleMap: playMode === "shuffle" ? shuffleOrder(filtered.length, idx) : null,
      replayToken: 0,
    });
    return true;
  },

  playTrack: (item, queue, index) => {
    const q = (queue && queue.length > 0 ? queue : [item]).filter((x) => x.mediaId > 0);
    if (q.length === 0) return false;
    const idx =
      index != null && index >= 0
        ? index
        : Math.max(
            0,
            q.findIndex((x) => x.mediaId === item.mediaId),
          );
    storeQueueSession(q);
    const playMode = get().playMode;
    set({
      active: true,
      albumId: item.albumId,
      queue: q,
      queueIndex: idx,
      playing: true,
      position: 0,
      duration: item.duration ?? q[idx]?.duration ?? 0,
      shuffleMap: playMode === "shuffle" ? shuffleOrder(q.length, idx) : null,
      replayToken: 0,
    });
    return true;
  },

  playQueue: (queue, startIndex = 0) => {
    const filtered = queue.filter((q) => q.mediaId > 0);
    if (filtered.length === 0) return false;
    const idx = Math.max(0, Math.min(startIndex, filtered.length - 1));
    storeQueueSession(filtered);
    const item = filtered[idx]!;
    const playMode = get().playMode;
    set({
      active: true,
      albumId: item.albumId,
      queue: filtered,
      queueIndex: idx,
      playing: true,
      position: 0,
      duration: item.duration ?? 0,
      shuffleMap: playMode === "shuffle" ? shuffleOrder(filtered.length, idx) : null,
      replayToken: 0,
    });
    return true;
  },

  play: () => set({ playing: true }),
  pause: () => set({ playing: false }),
  toggle: () => set((s) => ({ playing: !s.playing })),
  setPlaying: (playing) => set((s) => (s.playing === playing ? s : { playing })),

  next: () => {
    const { queue, queueIndex, playMode, shuffleMap } = get();
    if (queue.length === 0) return;
    let nextIndex = queueIndex + 1;
    if (playMode === "shuffle") {
      const map = shuffleMap ?? shuffleOrder(queue.length, queueIndex);
      const pos = map.indexOf(queueIndex);
      nextIndex = pos >= 0 && pos + 1 < map.length ? map[pos + 1]! : map[0] ?? 0;
      set({ shuffleMap: map });
    } else if (nextIndex >= queue.length) {
      if (playMode === "repeat-all") nextIndex = 0;
      else return;
    }
    const item = queue[nextIndex];
    if (!item) return;
    set({
      queueIndex: nextIndex,
      playing: true,
      position: 0,
      duration: item.duration ?? 0,
      albumId: item.albumId,
      replayToken: 0,
    });
  },

  prev: () => {
    const { queue, queueIndex, position, playMode, shuffleMap } = get();
    if (queue.length === 0) return;
    if (position > 3) {
      set({ position: 0, playing: true });
      return;
    }
    let prevIndex = queueIndex - 1;
    if (playMode === "shuffle") {
      const map = shuffleMap ?? shuffleOrder(queue.length, queueIndex);
      const pos = map.indexOf(queueIndex);
      prevIndex = pos > 0 ? map[pos - 1]! : map[map.length - 1] ?? 0;
      set({ shuffleMap: map });
    } else if (prevIndex < 0) {
      if (playMode === "repeat-all") prevIndex = queue.length - 1;
      else return;
    }
    const item = queue[prevIndex];
    if (!item) return;
    set({
      queueIndex: prevIndex,
      playing: true,
      position: 0,
      duration: item.duration ?? 0,
      albumId: item.albumId,
      replayToken: 0,
    });
  },

  seek: (sec) => set({ position: Math.max(0, sec) }),

  setVolume: (v) => {
    const vol = Math.max(0, Math.min(1, v));
    try {
      localStorage.setItem(VOLUME_KEY, String(vol));
    } catch {
      /* ignore */
    }
    set({ volume: vol, muted: vol === 0 });
  },

  toggleMute: () => set((s) => ({ muted: !s.muted })),

  cyclePlayMode: () => {
    const order: MusicPlayMode[] = ["sequential", "repeat-all", "shuffle", "repeat-one"];
    const cur = get().playMode;
    const next = order[(order.indexOf(cur) + 1) % order.length] ?? "sequential";
    try {
      localStorage.setItem(MODE_KEY, next);
    } catch {
      /* ignore */
    }
    set({ playMode: next, shuffleMap: next === "shuffle" ? shuffleOrder(get().queue.length, get().queueIndex) : null });
  },

  onTrackEnded: () => {
    const { playMode, queue, queueIndex } = get();
    if (queue.length === 0) {
      set({ playing: false, position: 0 });
      return;
    }
    if (playMode === "repeat-one") {
      set((s) => ({ playing: true, position: 0, replayToken: s.replayToken + 1 }));
      return;
    }
    if (playMode === "shuffle" || playMode === "repeat-all") {
      get().next();
      return;
    }
    const nextIndex = queueIndex + 1;
    if (nextIndex < queue.length) {
      const item = queue[nextIndex]!;
      set({
        queueIndex: nextIndex,
        playing: true,
        position: 0,
        duration: item.duration ?? 0,
        albumId: item.albumId,
        replayToken: 0,
      });
      return;
    }
    set({ playing: false, position: 0 });
  },

  syncPosition: (pos, dur) => {
    set((s) => ({
      position: pos,
      duration: dur > 0 ? dur : s.duration,
    }));
  },

  stop: () => {
    clearAlbumPlaySession();
    set({
      active: false,
      queue: [],
      queueIndex: 0,
      albumId: null,
      playing: false,
      position: 0,
      duration: 0,
      shuffleMap: null,
      replayToken: 0,
      fullscreen: false,
    });
  },
}));

export function currentMusicTrack(state: MusicPlayerState): MusicQueueItem | null {
  if (!state.active || state.queue.length === 0) return null;
  return state.queue[state.queueIndex] ?? null;
}
