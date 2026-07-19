import { act, render, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import PlayerPage from "../Player";
import { useAuthStore } from "../../store/auth";

const api = vi.hoisted(() => ({
  save: vi.fn(async (_mediaId: number, _payload: Record<string, unknown>) => ({ completed: false, auto_completed: false, effective_position: 0, stale: false })),
  start: vi.fn(async (_mediaId: number, _payload: Record<string, unknown>): Promise<void> => undefined),
  end: vi.fn(async (_mediaId: number, _payload: Record<string, unknown>): Promise<void> => undefined),
  endKeepalive: vi.fn(async (_mediaId: number, _payload: Record<string, unknown>): Promise<boolean> => true),
  saveKeepalive: vi.fn(async (_mediaId: number, _payload: Record<string, unknown>): Promise<boolean> => true),
}));
const xgInstances = vi.hoisted(() => [] as any[]);
const shakaInstances = vi.hoisted(() => [] as any[]);
const playlistNext = vi.hoisted(() => vi.fn(() => false));
let routeNavigate: ReturnType<typeof useNavigate>;
function RouteController() { routeNavigate = useNavigate(); return null; }

vi.mock("../../lib/playlistPlayback", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/playlistPlayback")>()),
  navigatePlaylistNext: playlistNext,
}));
vi.mock("../../api/client", () => ({
  fetchMediaSubtitles: vi.fn(async () => []),
  savePlaybackProgress: api.save,
  reportPlaybackStart: api.start,
  reportPlaybackEnd: api.end,
  reportPlaybackEndKeepalive: api.endKeepalive,
  savePlaybackProgressKeepalive: api.saveKeepalive,
}));
vi.mock("xgplayer-hls", () => ({ default: class {} }));
vi.mock("xgplayer-shaka", () => ({ default: class {} }));
vi.mock("xgplayer", () => {
  class XG {
    static TextTrack = class {};
    handlers = new Map<string, Array<(...args: any[]) => void>>();
    currentTime = 0;
    duration = 100;
    ended = false;
    root = document.createElement("div");
    constructor(public options: any) { xgInstances.push(this); }
    on(name: string, cb: (...args: any[]) => void) { this.handlers.set(name, [...(this.handlers.get(name) || []), cb]); }
    emit(name: string, value?: unknown) { for (const cb of this.handlers.get(name) || []) cb(value); }
    destroy() {}
  }
  return { default: XG, TextTrack: XG.TextTrack };
});
vi.mock("shaka-player/dist/shaka-player.ui.js", () => {
  class Shaka {
    static polyfill = { installAll: vi.fn() };
    static net = { NetworkingEngine: { RequestType: { LICENSE: 1 } } };
    handlers = new Map<string, (...args: any[]) => void>();
    constructor() { shakaInstances.push(this); }
    attach = vi.fn(async () => undefined);
    configure = vi.fn();
    load = vi.fn(async () => undefined);
    destroy = vi.fn(async () => undefined);
    addEventListener(name: string, cb: (...args: any[]) => void) { this.handlers.set(name, cb); }
    getNetworkingEngine() { return { registerRequestFilter: vi.fn(), registerResponseFilter: vi.fn() }; }
  }
  return { default: { Player: Shaka, polyfill: Shaka.polyfill, net: Shaka.net } };
});

const okJson = (value: unknown) => Promise.resolve({ ok: true, status: 200, json: async () => value, text: async () => "" } as Response);
function mount(plan: Record<string, unknown>, strict = false) {
  vi.stubGlobal("fetch", vi.fn((url: RequestInfo | URL) => {
    const raw = String(url);
    if (raw.includes("/hls?")) return okJson(plan);
    if (raw.includes("/preview?")) return okJson({ enabled: false });
    return okJson({});
  }));
  const node = <MemoryRouter initialEntries={["/player/42"]}><RouteController /><Routes><Route path="/player/:id" element={<PlayerPage />} /></Routes></MemoryRouter>;
  return render(strict ? <StrictMode>{node}</StrictMode> : node);
}
function evidence() { return api.save.mock.calls.map((call) => call[1] as any); }

beforeEach(() => {
  api.save.mockReset(); api.start.mockReset(); api.end.mockReset(); api.endKeepalive.mockReset(); api.saveKeepalive.mockReset(); playlistNext.mockReset();
  api.save.mockResolvedValue({ completed: false, auto_completed: false, effective_position: 0, stale: false });
  api.start.mockResolvedValue(undefined); api.end.mockResolvedValue(undefined); api.endKeepalive.mockResolvedValue(true); api.saveKeepalive.mockResolvedValue(true); playlistNext.mockReturnValue(false);
  xgInstances.length = 0; shakaInstances.length = 0;
  useAuthStore.setState({ token: "token", canPlay: true, playerPrefs: null });
  vi.useFakeTimers({ shouldAdvanceTime: true });
  vi.setSystemTime(new Date("2026-07-19T00:00:00Z"));
  delete (window as any).powerplayer;
  delete (window as any).PowerPlayer;
});

describe("Player playback evidence", () => {
  it("maps XGPlayer start, deduplicated seek, progress, and ended with dynamic JIT separation", async () => {
    mount({ mode: "jit_hls", hls_master: "/jit/session/a/master.m3u8", session_id: "jit-a", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); });
    act(() => { xg.currentTime = 20; xg.emit("seeking"); xg.emit("seeked"); xg.currentTime = 21; xg.emit("seeked"); });
    act(() => { vi.advanceTimersByTime(8999); xg.currentTime = 29; xg.emit("timeupdate"); });
    expect(evidence().filter((p) => p.event === "progress")).toHaveLength(0);
    act(() => { vi.advanceTimersByTime(2); xg.currentTime = 30; xg.emit("timeupdate"); });
    act(() => { xg.currentTime = 100; xg.ended = true; xg.emit("ended"); xg.emit("ended"); });
    await waitFor(() => expect(evidence()).toHaveLength(4));
    expect(api.start).toHaveBeenCalledTimes(1);
    const all = evidence();
    expect(all.map((p) => p.event)).toEqual(["seek", "seek", "progress", "ended"]);
    expect(all.map((p) => p.position)).toEqual([20, 21, 30, 100]);
    expect(all.map((p) => p.sequence)).toEqual([2, 3, 4, 5]);
    expect((api.start.mock.calls[0]![1] as any).sequence).toBe(1);
    expect(new Set([api.start.mock.calls[0]![1] as any, ...all].map((p) => p.session_id)).size).toBe(1);
    expect(all.every((p) => p.jit_session_id === "jit-a" && !Object.hasOwn(p, "completed") && Object.hasOwn(p, "session_id"))).toBe(true);
    expect(api.end).toHaveBeenCalledWith(42, { position: 100, jit_session_id: "jit-a" });
    expect(api.end.mock.calls[0]![1]).not.toHaveProperty("event");
    expect(api.end.mock.calls[0]![1]).not.toHaveProperty("sequence");
  });

  it("maps PowerPlayer callbacks and emits one ended evidence", async () => {
    const callbacks: Record<string, (...args: any[]) => void> = {};
    const pp: any = { destroy: vi.fn(), remove: vi.fn() };
    for (const name of ["onReady", "onSeek", "onTime", "onComplete", "onPause", "onError"]) pp[name] = (cb: any) => { callbacks[name] = cb; };
    (window as any).powerplayer = () => ({ setup: () => pp });
    mount({ mode: "native", playUrl: "/movie.mp4", player_engine_order: ["powerplayer"] });
    await waitFor(() => expect(callbacks.onTime).toBeTypeOf("function"));
    act(() => callbacks.onTime({ position: 1, duration: 100 }));
    act(() => callbacks.onSeek({ position: 20 }));
    act(() => { vi.advanceTimersByTime(8999); callbacks.onTime({ position: 29, duration: 100 }); });
    expect(evidence().filter((p) => p.event === "progress")).toHaveLength(0);
    act(() => { vi.advanceTimersByTime(2); callbacks.onTime({ position: 30, duration: 100 }); });
    act(() => callbacks.onComplete({ position: 100, duration: 100 }));
    await waitFor(() => expect(evidence()).toHaveLength(3));
    expect(evidence().map((p: any) => p.event)).toEqual(["seek", "progress", "ended"]);
    act(() => callbacks.onComplete({ position: 100, duration: 100 }));
    expect(evidence().filter((p) => p.event === "ended")).toHaveLength(1);
  });

  it("maps native Shaka video events", async () => {
    mount({ mode: "hls_drm", hls_master: "/drm.m3u8", status: "done", player_engine_order: ["shaka"], drm: { widevine_license_url: "/license" } });
    await waitFor(() => expect(shakaInstances).toHaveLength(1));
    const video = document.querySelector("video")!;
    Object.defineProperties(video, { currentTime: { value: 1, writable: true }, duration: { value: 100, configurable: true }, ended: { value: false, writable: true } });
    act(() => video.dispatchEvent(new Event("play")));
    act(() => { video.currentTime = 20; video.dispatchEvent(new Event("seeking")); });
    act(() => { vi.advanceTimersByTime(8999); video.currentTime = 29; video.dispatchEvent(new Event("timeupdate")); });
    expect(evidence().filter((p) => p.event === "progress")).toHaveLength(0);
    act(() => { vi.advanceTimersByTime(2); video.currentTime = 30; video.dispatchEvent(new Event("timeupdate")); });
    act(() => { video.currentTime = 100; (video as any).ended = true; video.dispatchEvent(new Event("ended")); });
    await waitFor(() => expect(evidence()).toHaveLength(3));
    expect(evidence().map((p: any) => p.event)).toEqual(["seek", "progress", "ended"]);
    act(() => video.dispatchEvent(new Event("ended")));
    expect(evidence().filter((p) => p.event === "ended")).toHaveLength(1);
  });

  it("fences stale cleanup and sends final progress only from the owning started generation", async () => {
    const view = mount({ mode: "native", playUrl: "/movie.mp4", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 5; xg.emit("play"); });
    view.unmount();
    await waitFor(() => expect(evidence().map((p) => p.event)).toEqual(["progress"]));
    expect(evidence()[0]).not.toHaveProperty("completed");
  });

  it("prevents an old StrictMode cleanup from emitting after the newer generation owns playback", async () => {
    mount({ mode: "native", playUrl: "/movie.mp4", player_engine_order: ["xgplayer"] }, true);
    await waitFor(() => expect(xgInstances.length).toBeGreaterThanOrEqual(1));
    const current = xgInstances.at(-1)!;
    act(() => { current.currentTime = 5; current.emit("play"); });
    await waitFor(() => expect(evidence()).toEqual([]));
    expect(api.end).not.toHaveBeenCalled();
  });

  it("keeps server-completed state without emitting legacy clear payloads", async () => {
    api.save.mockResolvedValueOnce({ completed: true, auto_completed: true, effective_position: 90, stale: false });
    const view = mount({ mode: "native", playUrl: "/movie.mp4", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); vi.advanceTimersByTime(9001); xg.currentTime = 90; xg.emit("timeupdate"); });
    await waitFor(() => expect(api.save).toHaveBeenCalledTimes(1));
    view.unmount();
    await waitFor(() => expect(api.save).toHaveBeenCalledTimes(2));
    expect(evidence().every((p) => !Object.hasOwn(p, "completed"))).toBe(true);
  });

  it("preserves one application start and coherent evidence through the actual XG error fallback callback", async () => {
    mount({ mode: "native", playUrl: "/broken.mp4", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const first = xgInstances[0];
    act(() => { first.currentTime = 5; first.emit("play"); first.emit("error"); });
    await waitFor(() => expect(xgInstances).toHaveLength(2));
    const replacement = xgInstances[1];
    act(() => { first.currentTime = 70; first.emit("seeking"); });
    expect(evidence()).toEqual([]);
    act(() => { replacement.currentTime = 6; replacement.emit("play"); replacement.currentTime = 25; replacement.emit("seeking"); });
    act(() => { vi.advanceTimersByTime(9001); replacement.currentTime = 35; replacement.emit("timeupdate"); });
    act(() => { replacement.currentTime = 100; replacement.ended = true; replacement.emit("ended"); });
    await waitFor(() => expect(evidence().map((p) => p.event)).toEqual(["seek", "progress", "ended"]));
    expect(api.start).toHaveBeenCalledTimes(1);
    expect((api.start.mock.calls[0]![1] as any).sequence).toBe(1);
    expect(evidence().map((p) => p.sequence)).toEqual([2, 3, 4]);
  });

  it("supports the PowerPlayer constructor branch with the shared callback mapping", async () => {
    const callbacks: Record<string, (...args: any[]) => void> = {};
    class PowerPlayerConstructor {
      onReady(cb: any) { callbacks.onReady = cb; }
      onSeek(cb: any) { callbacks.onSeek = cb; }
      onTime(cb: any) { callbacks.onTime = cb; }
      onComplete(cb: any) { callbacks.onComplete = cb; }
      onPause(cb: any) { callbacks.onPause = cb; }
      onError(cb: any) { callbacks.onError = cb; }
      destroy() {}
      remove() {}
    }
    (window as any).PowerPlayer = PowerPlayerConstructor;
    mount({ mode: "native", playUrl: "/movie.mp4", player_engine_order: ["powerplayer"] });
    await waitFor(() => expect(callbacks.onTime).toBeTypeOf("function"));
    act(() => callbacks.onTime({ position: 1, duration: 100 }));
    act(() => callbacks.onSeek({ position: 10 }));
    expect(api.start).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(evidence().map((p) => p.event)).toEqual(["seek"]));
  });

  it("uses the shared XG adapter for the xgplayer-shaka plugin branch", async () => {
    mount({ mode: "hls_drm", hls_master: "/drm.m3u8", status: "done", player_engine_order: ["xgplayer"], drm: { widevine_license_url: "/license" } });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    expect(xg.options.plugins).toHaveLength(1);
    act(() => { xg.currentTime = 1; xg.emit("play"); xg.currentTime = 9; xg.emit("seeking"); });
    expect(api.start).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(evidence().map((p) => p.event)).toEqual(["seek"]));
  });

  it("uses activity-only cleanup payload after server completion without resetting completion", async () => {
    api.save.mockResolvedValueOnce({ completed: true, auto_completed: true, effective_position: 90, stale: false });
    const view = mount({ mode: "jit_hls", hls_master: "/jit/master.m3u8", session_id: "jit-cleanup", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); vi.advanceTimersByTime(9001); xg.currentTime = 90; xg.emit("timeupdate"); });
    await waitFor(() => expect(api.save).toHaveBeenCalledTimes(1));
    view.unmount();
    await waitFor(() => expect(api.end).toHaveBeenCalledTimes(1));
    expect(api.end.mock.calls[0]![1]).toEqual({ position: 90, jit_session_id: "jit-cleanup" });
    expect(evidence().map((p) => p.event)).toEqual(["progress", "progress"]);
    expect(evidence().every((p) => !Object.hasOwn(p, "completed"))).toBe(true);
    expect(evidence().filter((p) => p.event === "ended")).toHaveLength(0);
  });

  it("creates distinct application sessions and restarts sequence for new StrictMode mounts", async () => {
    const firstView = mount({ mode: "native", playUrl: "/one.mp4", player_engine_order: ["xgplayer"] }, true);
    await waitFor(() => expect(xgInstances.length).toBeGreaterThanOrEqual(1));
    const first = xgInstances.at(-1)!;
    act(() => { first.currentTime = 1; first.emit("play"); });
    const firstStart = api.start.mock.calls.at(-1)![1] as any;
    firstView.unmount();
    await waitFor(() => expect(api.end).toHaveBeenCalledTimes(1));
    api.end.mockClear();
    mount({ mode: "native", playUrl: "/two.mp4", player_engine_order: ["xgplayer"] }, true);
    await waitFor(() => expect(xgInstances.length).toBeGreaterThanOrEqual(2));
    const second = xgInstances.at(-1)!;
    act(() => { second.currentTime = 1; second.emit("play"); });
    const secondStart = api.start.mock.calls.at(-1)![1] as any;
    expect(secondStart.session_id).not.toBe(firstStart.session_id);
    expect(firstStart.sequence).toBe(1);
    expect(secondStart.sequence).toBe(1);
  });


  it("serializes evidence behind a successful deferred start request", async () => {
    let resolveStart!: () => void;
    api.start.mockImplementationOnce(() => new Promise<void>((resolve) => { resolveStart = resolve; }));
    mount({ mode: "native", playUrl: "/movie.mp4", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); xg.currentTime = 20; xg.emit("seeking"); vi.advanceTimersByTime(9001); xg.currentTime = 30; xg.emit("timeupdate"); });
    expect(api.start).toHaveBeenCalledTimes(1);
    expect(api.save).not.toHaveBeenCalled();
    resolveStart();
    await waitFor(() => expect(evidence().map((p) => p.event)).toEqual(["seek", "progress"]));
    expect(evidence().map((p) => p.sequence)).toEqual([2, 3]);
  });

  it("drops orphan evidence after start fails but still sends activity cleanup", async () => {
    api.start.mockRejectedValueOnce(new Error("start failed"));
    const view = mount({ mode: "jit_hls", hls_master: "/jit/master.m3u8", session_id: "jit-fail", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); xg.currentTime = 20; xg.emit("seeking"); });
    await waitFor(() => expect(api.start).toHaveBeenCalledTimes(1));
    view.unmount();
    await waitFor(() => expect(api.end).toHaveBeenCalledWith(42, { position: 20, jit_session_id: "jit-fail" }));
    expect(api.save).not.toHaveBeenCalled();
  });

  it("persists ended before activity shutdown and navigation", async () => {
    playlistNext.mockReturnValue(true);
    let resolveEnded!: (value: any) => void;
    api.save.mockImplementation(async (_id, payload) => {
      if (payload.event === "ended") return new Promise((resolve) => { resolveEnded = resolve; });
      return { completed: false, auto_completed: false, effective_position: Number(payload.position), stale: false };
    });
    mount({ mode: "native", playUrl: "/movie.mp4", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); xg.currentTime = 100; xg.ended = true; xg.emit("ended"); });
    await waitFor(() => expect(api.save).toHaveBeenCalledTimes(1));
    expect(api.end).not.toHaveBeenCalled();
    expect(playlistNext).not.toHaveBeenCalled();
    resolveEnded({ completed: true, auto_completed: false, effective_position: 100, stale: false });
    await waitFor(() => expect(api.end).toHaveBeenCalledTimes(1));
    expect(playlistNext).toHaveBeenCalledTimes(1);
  });

  it("attempts activity shutdown and navigation when ended persistence fails", async () => {
    playlistNext.mockReturnValue(true);
    api.save.mockRejectedValueOnce(new Error("ended failed"));
    mount({ mode: "native", playUrl: "/movie.mp4", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); xg.currentTime = 100; xg.ended = true; xg.emit("ended"); });
    await waitFor(() => expect(api.end).toHaveBeenCalledTimes(1));
    expect(playlistNext).toHaveBeenCalledTimes(1);
  });


  it("uses monotonic time for progress throttling across backward wall-clock changes", async () => {
    let monotonic = 1000;
    vi.spyOn(performance, "now").mockImplementation(() => monotonic);
    mount({ mode: "native", playUrl: "/movie.mp4", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); });
    vi.setSystemTime(new Date("2000-01-01T00:00:00Z"));
    monotonic += 9001;
    act(() => { xg.currentTime = 20; xg.emit("timeupdate"); });
    await waitFor(() => expect(evidence().map((p) => p.event)).toEqual(["progress"]));
  });

  it("ignores delayed errors from a stale standalone Shaka instance", async () => {
    mount({ mode: "hls_drm", hls_master: "/drm.m3u8", status: "done", player_engine_order: ["shaka"], drm: { widevine_license_url: "/license" } });
    await waitFor(() => expect(shakaInstances).toHaveLength(1));
    const stale = shakaInstances[0];
    act(() => routeNavigate("/player/43"));
    await waitFor(() => expect(shakaInstances).toHaveLength(2));
    stale.handlers.get("error")?.({ detail: { data: [null, null, "AUDIO_RENDERER_ERROR"] } });
    await Promise.resolve();
    expect(shakaInstances).toHaveLength(2);
  });


  it("captures one old-generation teardown synchronously on mid change", async () => {
    mount({ mode: "jit_hls", hls_master: "/jit/master.m3u8", session_id: "jit-old", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const old = xgInstances[0];
    act(() => { old.currentTime = 1; old.emit("play"); old.currentTime = 40; });
    const oldSession = (api.start.mock.calls[0]![1] as any).session_id;
    act(() => routeNavigate("/player/43"));
    await waitFor(() => expect(api.end).toHaveBeenCalledWith(42, { position: 40, jit_session_id: "jit-old" }));
    await waitFor(() => expect(evidence().some((p) => p.event === "progress" && p.position === 40)).toBe(true));
    await waitFor(() => expect(xgInstances).toHaveLength(2));
    const current = xgInstances[1];
    act(() => { current.currentTime = 1; current.emit("play"); });
    const newSession = (api.start.mock.calls.at(-1)![1] as any).session_id;
    expect(newSession).not.toBe(oldSession);
    expect(api.end.mock.calls.filter((call) => call[0] === 42)).toHaveLength(1);
  });

  it("resets fallback recovery allowance for the next media generation", async () => {
    mount({ mode: "native", playUrl: "/broken.mp4", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    act(() => xgInstances[0].emit("error"));
    await waitFor(() => expect(xgInstances).toHaveLength(2));
    act(() => routeNavigate("/player/43"));
    await waitFor(() => expect(xgInstances).toHaveLength(3));
    act(() => xgInstances[2].emit("error"));
    await waitFor(() => expect(xgInstances).toHaveLength(4));
  });


  it("orders route cleanup progress before activity shutdown settles", async () => {
    let resolveProgress!: (value: any) => void;
    api.save.mockImplementationOnce(() => new Promise((resolve) => { resolveProgress = resolve; }));
    const view = mount({ mode: "jit_hls", hls_master: "/jit/master.m3u8", session_id: "jit-order", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); xg.currentTime = 40; });
    view.unmount();
    await waitFor(() => expect(api.save).toHaveBeenCalledTimes(1));
    expect(api.end).not.toHaveBeenCalled();
    resolveProgress({ completed: false, auto_completed: false, effective_position: 40, stale: false });
    await waitFor(() => expect(api.end).toHaveBeenCalledWith(42, { position: 40, jit_session_id: "jit-order" }));
  });

  it("attempts route activity shutdown after final progress failure", async () => {
    api.save.mockRejectedValueOnce(new Error("final failed"));
    const view = mount({ mode: "jit_hls", hls_master: "/jit/master.m3u8", session_id: "jit-final-fail", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); xg.currentTime = 40; });
    view.unmount();
    await waitFor(() => expect(api.end).toHaveBeenCalledWith(42, { position: 40, jit_session_id: "jit-final-fail" }));
  });

  it("uses one authenticated keepalive teardown on pagehide without duplicate route shutdown", async () => {
    const view = mount({ mode: "jit_hls", hls_master: "/jit/master.m3u8", session_id: "jit-hide", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); xg.currentTime = 55; window.dispatchEvent(new Event("pagehide")); });
    await waitFor(() => expect(api.saveKeepalive).toHaveBeenCalledTimes(1));
    const progress = api.saveKeepalive.mock.calls[0]![1] as any;
    expect(progress).toMatchObject({ position: 55, event: "progress", sequence: 2, jit_session_id: "jit-hide" });
    expect(progress.session_id).toBe((api.start.mock.calls[0]![1] as any).session_id);
    expect(api.endKeepalive).toHaveBeenCalledWith(42, { position: 55, jit_session_id: "jit-hide" });
    expect(api.saveKeepalive.mock.invocationCallOrder[0]).toBeLessThan(api.endKeepalive.mock.invocationCallOrder[0]!);
    window.dispatchEvent(new Event("beforeunload"));
    view.unmount();
    await Promise.resolve();
    expect(api.saveKeepalive).toHaveBeenCalledTimes(1);
    expect(api.endKeepalive).toHaveBeenCalledTimes(1);
    expect(api.end).not.toHaveBeenCalled();
  });


  it("ignores delayed HTML video errors from an old standalone Shaka generation", async () => {
    mount({ mode: "hls_drm", hls_master: "/drm.m3u8", status: "done", player_engine_order: ["shaka"], drm: { widevine_license_url: "/license" } });
    await waitFor(() => expect(shakaInstances).toHaveLength(1));
    const oldVideo = document.querySelector("video")!;
    act(() => routeNavigate("/player/43"));
    await waitFor(() => expect(shakaInstances).toHaveLength(2));
    const instanceCount = shakaInstances.length;
    oldVideo.dispatchEvent(new Event("error"));
    await Promise.resolve();
    expect(shakaInstances).toHaveLength(instanceCount);
    expect(xgInstances).toHaveLength(0);
  });


  it("does not send unload keepalives before start or after natural ended", async () => {
    const view = mount({ mode: "native", playUrl: "/movie.mp4", player_engine_order: ["xgplayer"] });
    await waitFor(() => expect(xgInstances).toHaveLength(1));
    window.dispatchEvent(new Event("pagehide"));
    expect(api.saveKeepalive).not.toHaveBeenCalled();
    expect(api.endKeepalive).not.toHaveBeenCalled();
    const xg = xgInstances[0];
    act(() => { xg.currentTime = 1; xg.emit("play"); xg.currentTime = 100; xg.ended = true; xg.emit("ended"); });
    await waitFor(() => expect(evidence().some((payload) => payload.event === "ended")).toBe(true));
    window.dispatchEvent(new Event("beforeunload"));
    expect(api.saveKeepalive).not.toHaveBeenCalled();
    expect(api.endKeepalive).not.toHaveBeenCalled();
    view.unmount();
  });

});
