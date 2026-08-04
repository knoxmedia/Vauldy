import { afterEach, describe, expect, it, vi } from "vitest";
import type { HistoryItem, Library, MediaItem } from "../../api/client";
import {
  HOME_LIBRARY_ACTIVE_POLL_MS,
  HOME_LIBRARY_IDLE_POLL_MS,
  HOME_RECENT_POLL_MS,
  HomeRequestCoordinator,
} from "../homeRequestCoordinator";
import type { HomeRecentSection } from "../homeRecentSections";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

const section: HomeRecentSection = { key: "movie", title: "Movies", libTypes: ["movie"], landscape: false };
const library: Library = { id: 1, name: "Movies", type: "movie", path: "", auto_scan: 0, scraper: "", created_at: "" };
const history = { media_id: 1, title: "current" } as HistoryItem;
const media = { id: 1, library_id: 1, file_id: "f", title: "current", file_path: "p", file_type: "video", duration: 0, width: 0, height: 0, format: "", status: "active" } as MediaItem;
const tick = async () => { await Promise.resolve(); await Promise.resolve(); };

function makeCoordinator(overrides: Partial<ConstructorParameters<typeof HomeRequestCoordinator>[0]> = {}) {
  return new HomeRequestCoordinator({
    fetchLibraries: vi.fn().mockResolvedValue([library]),
    fetchHistory: vi.fn().mockResolvedValue([history]),
    loadRecent: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  }, { onLoading: vi.fn(), onLibraries: vi.fn(), onHistory: vi.fn(), onSection: vi.fn() });
}

afterEach(() => {
  vi.useRealTimers();
  Object.defineProperty(document, "hidden", { configurable: true, value: false });
});

describe("HomeRequestCoordinator", () => {
  it("ends full-page loading on the first successful history response", async () => {
    const libraries = deferred<Library[]>();
    const coordinator = makeCoordinator({ fetchLibraries: vi.fn(() => libraries.promise) });
    coordinator.start([section]);
    await tick();
    expect(coordinator.handlers.onHistory).toHaveBeenCalledWith([history]);
    expect(coordinator.handlers.onLoading).toHaveBeenLastCalledWith(false);
    libraries.resolve([library]);
    coordinator.stop();
  });

  it("ends full-page loading on the first successful libraries response while recent is pending", async () => {
    const historyRequest = deferred<HistoryItem[]>();
    const recentRequest = deferred<void>();
    const coordinator = makeCoordinator({
      fetchHistory: vi.fn(() => historyRequest.promise),
      loadRecent: vi.fn(() => recentRequest.promise),
    });
    coordinator.start([section]);
    await tick();
    expect(coordinator.handlers.onLibraries).toHaveBeenCalledWith([library]);
    expect(coordinator.handlers.onLoading).toHaveBeenLastCalledWith(false);
    historyRequest.resolve([]);
    recentRequest.resolve();
    coordinator.stop();
  });

  it("ends loading after both base requests fail without committing data", async () => {
    const coordinator = makeCoordinator({
      fetchLibraries: vi.fn().mockRejectedValue(new Error("libraries")),
      fetchHistory: vi.fn().mockRejectedValue(new Error("history")),
    });
    coordinator.start([section]);
    await tick();
    expect(coordinator.handlers.onLoading).toHaveBeenLastCalledWith(false);
    expect(coordinator.handlers.onLibraries).not.toHaveBeenCalled();
    expect(coordinator.handlers.onHistory).not.toHaveBeenCalled();
    coordinator.stop();
  });

  it("ends loading on the first recent section callback", async () => {
    const historyRequest = deferred<HistoryItem[]>();
    const loadRecent = vi.fn(async (_libs, _sections, _signal, onSection) => {
      onSection("movie", [media]);
    });
    const coordinator = makeCoordinator({ fetchHistory: vi.fn(() => historyRequest.promise), loadRecent });
    coordinator.start([section]);
    await tick();
    expect(coordinator.handlers.onSection).toHaveBeenCalledWith("movie", [media]);
    expect(coordinator.handlers.onLoading).toHaveBeenLastCalledWith(false);
    historyRequest.resolve([]);
    coordinator.stop();
  });

  it("aborts and drops old history and recent callbacks after restart", async () => {
    const oldHistory = deferred<HistoryItem[]>();
    let oldRecentCallback: ((key: string, items: MediaItem[]) => void) | undefined;
    const fetchHistory = vi.fn().mockImplementationOnce(() => oldHistory.promise).mockResolvedValueOnce([]);
    const loadRecent = vi.fn().mockImplementationOnce((_libs, _sections, _signal, callback) => {
      oldRecentCallback = callback;
      return new Promise<void>(() => undefined);
    }).mockResolvedValueOnce(undefined);
    const coordinator = makeCoordinator({ fetchHistory, loadRecent });
    coordinator.start([section]);
    await tick();
    const oldHistorySignal = fetchHistory.mock.calls[0]![0] as AbortSignal;
    coordinator.start([section]);
    expect(oldHistorySignal.aborted).toBe(true);
    oldHistory.resolve([{ ...history, title: "stale history" }]);
    oldRecentCallback?.("movie", [{ ...media, title: "stale recent" }]);
    await tick();
    expect(coordinator.handlers.onHistory).not.toHaveBeenCalledWith([expect.objectContaining({ title: "stale history" })]);
    expect(coordinator.handlers.onSection).not.toHaveBeenCalledWith("movie", [expect.objectContaining({ title: "stale recent" })]);
    coordinator.stop();
  });

  it("uses active and idle library delays after request completion", async () => {
    vi.useFakeTimers();
    const fetchLibraries = vi.fn()
      .mockResolvedValueOnce([{ ...library, scan_status: "running" }])
      .mockResolvedValueOnce([{ ...library, scan_status: "idle" }])
      .mockResolvedValue([{ ...library, scan_status: "idle" }]);
    const coordinator = makeCoordinator({ fetchLibraries });
    coordinator.start([section]);
    await tick();
    await vi.advanceTimersByTimeAsync(HOME_LIBRARY_ACTIVE_POLL_MS - 1);
    expect(fetchLibraries).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetchLibraries).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(HOME_LIBRARY_IDLE_POLL_MS - 1);
    expect(fetchLibraries).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetchLibraries).toHaveBeenCalledTimes(3);
    coordinator.stop();
  });

  it("schedules the next recent refresh only after completion", async () => {
    vi.useFakeTimers();
    const refresh = deferred<void>();
    const loadRecent = vi.fn().mockResolvedValueOnce(undefined).mockImplementationOnce(() => refresh.promise).mockResolvedValue(undefined);
    const coordinator = makeCoordinator({ loadRecent });
    coordinator.start([section]);
    await tick();
    await vi.advanceTimersByTimeAsync(HOME_RECENT_POLL_MS);
    expect(loadRecent).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(HOME_RECENT_POLL_MS * 2);
    expect(loadRecent).toHaveBeenCalledTimes(2);
    refresh.resolve();
    await tick();
    await vi.advanceTimersByTimeAsync(HOME_RECENT_POLL_MS - 1);
    expect(loadRecent).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(loadRecent).toHaveBeenCalledTimes(3);
    coordinator.stop();
  });

  it("pauses timers while hidden and refreshes once on visibility return", async () => {
    vi.useFakeTimers();
    let hidden = false;
    Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
    const loadRecent = vi.fn().mockResolvedValue(undefined);
    const fetchLibraries = vi.fn().mockResolvedValue([library]);
    const coordinator = makeCoordinator({ fetchLibraries, loadRecent });
    coordinator.start([section]);
    await tick();
    hidden = true;
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(HOME_LIBRARY_IDLE_POLL_MS + HOME_RECENT_POLL_MS);
    expect(fetchLibraries).toHaveBeenCalledTimes(1);
    expect(loadRecent).toHaveBeenCalledTimes(1);
    hidden = false;
    document.dispatchEvent(new Event("visibilitychange"));
    await tick();
    expect(fetchLibraries).toHaveBeenCalledTimes(2);
    expect(loadRecent).toHaveBeenCalledTimes(2);
    coordinator.stop();
  });


  it("coalesces visibility and focus recovery without a trailing dirty rerun", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000);
    let hidden = false;
    Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
    const focusLibraries = deferred<Library[]>();
    const focusRecent = deferred<void>();
    const fetchLibraries = vi.fn().mockResolvedValueOnce([library]).mockImplementationOnce(() => focusLibraries.promise).mockResolvedValue([library]);
    const loadRecent = vi.fn().mockResolvedValueOnce(undefined).mockImplementationOnce(() => focusRecent.promise).mockResolvedValue(undefined);
    const coordinator = makeCoordinator({ fetchLibraries, loadRecent });
    coordinator.start([section]);
    await tick();

    hidden = true;
    document.dispatchEvent(new Event("visibilitychange"));
    hidden = false;
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new Event("focus"));
    await tick();
    expect(fetchLibraries).toHaveBeenCalledTimes(2);
    expect(loadRecent).toHaveBeenCalledTimes(2);

    focusLibraries.resolve([library]);
    focusRecent.resolve();
    await tick();
    expect(fetchLibraries).toHaveBeenCalledTimes(2);
    expect(loadRecent).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(251);
    window.dispatchEvent(new Event("focus"));
    await tick();
    expect(fetchLibraries).toHaveBeenCalledTimes(3);
    expect(loadRecent).toHaveBeenCalledTimes(3);
    coordinator.stop();
  });

  it("backs off library errors from the last active delay and resets after success", async () => {
    vi.useFakeTimers();
    const fetchLibraries = vi.fn()
      .mockResolvedValueOnce([{ ...library, scan_status: "running" }])
      .mockRejectedValueOnce(new Error("one"))
      .mockRejectedValueOnce(new Error("two"))
      .mockResolvedValue([{ ...library, scan_status: "running" }]);
    const coordinator = makeCoordinator({ fetchLibraries });
    coordinator.start([section]);
    await tick();
    await vi.advanceTimersByTimeAsync(3_000);
    expect(fetchLibraries).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(5_999);
    expect(fetchLibraries).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetchLibraries).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(11_999);
    expect(fetchLibraries).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetchLibraries).toHaveBeenCalledTimes(4);
    await vi.advanceTimersByTimeAsync(3_000);
    expect(fetchLibraries).toHaveBeenCalledTimes(5);
    coordinator.stop();
  });

  it("backs off idle libraries and recent errors independently up to sixty seconds", async () => {
    vi.useFakeTimers();
    const fetchLibraries = vi.fn().mockResolvedValueOnce([{ ...library, scan_status: "idle" }]).mockRejectedValue(new Error("library"));
    const loadRecent = vi.fn().mockResolvedValueOnce(undefined).mockRejectedValue(new Error("recent"));
    const coordinator = makeCoordinator({ fetchLibraries, loadRecent });
    coordinator.start([section]);
    await tick();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(loadRecent).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(fetchLibraries).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(15_000);
    expect(loadRecent).toHaveBeenCalledTimes(3);
    expect(fetchLibraries).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(15_000);
    expect(fetchLibraries).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(25_000);
    expect(loadRecent).toHaveBeenCalledTimes(4);
    expect(fetchLibraries).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(35_000);
    expect(fetchLibraries).toHaveBeenCalledTimes(4);
    coordinator.stop();
  });

  it("coalesces repeated mutation refreshes without overlapping history or recent", async () => {
    const mutationHistory = deferred<HistoryItem[]>();
    const mutationRecent = deferred<void>();
    const fetchHistory = vi.fn().mockResolvedValueOnce([]).mockImplementationOnce(() => mutationHistory.promise).mockResolvedValue([]);
    const loadRecent = vi.fn().mockResolvedValueOnce(undefined).mockImplementationOnce(() => mutationRecent.promise).mockResolvedValue(undefined);
    const coordinator = makeCoordinator({ fetchHistory, loadRecent });
    coordinator.start([section]);
    await vi.waitFor(() => expect(loadRecent).toHaveBeenCalledTimes(1));
    await tick();
    const first = coordinator.refreshAfterMutation();
    const second = coordinator.refreshAfterMutation();
    await tick();
    expect(fetchHistory).toHaveBeenCalledTimes(2);
    expect(loadRecent).toHaveBeenCalledTimes(2);
    mutationHistory.resolve([]);
    mutationRecent.resolve();
    await first;
    await second;
    expect(fetchHistory).toHaveBeenCalledTimes(3);
    expect(loadRecent).toHaveBeenCalledTimes(3);
    coordinator.stop();
  });

  it("aborts mutation refresh and drops its old history and recent callbacks on stop", async () => {
    const mutationHistory = deferred<HistoryItem[]>();
    let mutationRecentCallback: ((key: string, items: MediaItem[]) => void) | undefined;
    const fetchHistory = vi.fn().mockResolvedValueOnce([]).mockImplementationOnce(() => mutationHistory.promise);
    const loadRecent = vi.fn().mockResolvedValueOnce(undefined).mockImplementationOnce((_libs, _sections, _signal, callback) => {
      mutationRecentCallback = callback;
      return new Promise<void>(() => undefined);
    });
    const coordinator = makeCoordinator({ fetchHistory, loadRecent });
    coordinator.start([section]);
    await vi.waitFor(() => expect(loadRecent).toHaveBeenCalledTimes(1));
    await tick();
    void coordinator.refreshAfterMutation();
    await tick();
    const mutationSignal = fetchHistory.mock.calls[1]![0] as AbortSignal;
    coordinator.stop();
    expect(mutationSignal.aborted).toBe(true);
    mutationHistory.resolve([{ ...history, title: "stale mutation history" }]);
    mutationRecentCallback?.("movie", [{ ...media, title: "stale mutation recent" }]);
    await tick();
    expect(coordinator.handlers.onHistory).not.toHaveBeenCalledWith([expect.objectContaining({ title: "stale mutation history" })]);
    expect(coordinator.handlers.onSection).not.toHaveBeenCalledWith("movie", [expect.objectContaining({ title: "stale mutation recent" })]);
  });

  it("shares recent in-flight guard between polling and mutation refresh", async () => {
    vi.useFakeTimers();
    const pollingRecent = deferred<void>();
    const loadRecent = vi.fn().mockResolvedValueOnce(undefined).mockImplementationOnce(() => pollingRecent.promise).mockResolvedValue(undefined);
    const fetchHistory = vi.fn().mockResolvedValue([]);
    const coordinator = makeCoordinator({ fetchHistory, loadRecent });
    coordinator.start([section]);
    await tick();
    await vi.advanceTimersByTimeAsync(HOME_RECENT_POLL_MS);
    expect(loadRecent).toHaveBeenCalledTimes(2);
    void coordinator.refreshAfterMutation();
    await tick();
    expect(loadRecent).toHaveBeenCalledTimes(2);
    pollingRecent.resolve();
    await tick();
    coordinator.stop();
  });
  it("does not reload recent for scan-status-only library polling updates", async () => {
    vi.useFakeTimers();
    const fetchLibraries = vi.fn()
      .mockResolvedValueOnce([{ ...library, scan_status: "running", scan_processed_count: 1 }])
      .mockResolvedValueOnce([{ ...library, scan_status: "running", scan_processed_count: 2 }]);
    const loadRecent = vi.fn().mockResolvedValue(undefined);
    const coordinator = makeCoordinator({ fetchLibraries, loadRecent });
    coordinator.start([section]);
    await tick();
    expect(loadRecent).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(HOME_LIBRARY_ACTIVE_POLL_MS);
    expect(fetchLibraries).toHaveBeenCalledTimes(2);
    expect(loadRecent).toHaveBeenCalledTimes(1);
    coordinator.stop();
  });

  it("keeps every coalesced mutation promise pending through the dirty rerun", async () => {
    const firstHistory = deferred<HistoryItem[]>();
    const secondHistory = deferred<HistoryItem[]>();
    const firstRecent = deferred<void>();
    const secondRecent = deferred<void>();
    const fetchHistory = vi.fn().mockResolvedValueOnce([])
      .mockImplementationOnce(() => firstHistory.promise)
      .mockImplementationOnce(() => secondHistory.promise);
    const loadRecent = vi.fn().mockResolvedValueOnce(undefined)
      .mockImplementationOnce(() => firstRecent.promise)
      .mockImplementationOnce(() => secondRecent.promise);
    const coordinator = makeCoordinator({ fetchHistory, loadRecent });
    coordinator.start([section]);
    await tick();
    let firstResolved = false;
    let secondResolved = false;
    const first = coordinator.refreshAfterMutation().then(() => { firstResolved = true; });
    const second = coordinator.refreshAfterMutation().then(() => { secondResolved = true; });
    firstHistory.resolve([]);
    firstRecent.resolve();
    await vi.waitFor(() => expect(fetchHistory).toHaveBeenCalledTimes(3));
    expect(loadRecent).toHaveBeenCalledTimes(3);
    expect(firstResolved).toBe(false);
    expect(secondResolved).toBe(false);
    secondHistory.resolve([]);
    secondRecent.resolve();
    await Promise.all([first, second]);
    expect(firstResolved).toBe(true);
    expect(secondResolved).toBe(true);
    coordinator.stop();
  });
  it("waits for initialization snapshots then forces a post-mutation history and recent cycle", async () => {
    const initLibraries = deferred<Library[]>();
    const initHistory = deferred<HistoryItem[]>();
    const initRecent = deferred<void>();
    const freshHistory = deferred<HistoryItem[]>();
    const freshRecent = deferred<void>();
    const fetchLibraries = vi.fn(() => initLibraries.promise);
    const fetchHistory = vi.fn().mockImplementationOnce(() => initHistory.promise).mockImplementationOnce(() => freshHistory.promise);
    const loadRecent = vi.fn().mockImplementationOnce(() => initRecent.promise).mockImplementationOnce(() => freshRecent.promise);
    const coordinator = makeCoordinator({ fetchLibraries, fetchHistory, loadRecent });
    coordinator.start([section]);
    let resolved = false;
    const mutation = coordinator.refreshAfterMutation().then(() => { resolved = true; });
    initLibraries.resolve([library]);
    await tick();
    expect(loadRecent).toHaveBeenCalledTimes(1);
    expect(fetchHistory).toHaveBeenCalledTimes(1);
    initHistory.resolve([{ ...history, title: "old history" }]);
    initRecent.resolve();
    await vi.waitFor(() => expect(fetchHistory).toHaveBeenCalledTimes(2));
    expect(loadRecent).toHaveBeenCalledTimes(2);
    expect(resolved).toBe(false);
    freshHistory.resolve([{ ...history, title: "fresh history" }]);
    freshRecent.resolve();
    await mutation;
    expect(resolved).toBe(true);
    coordinator.stop();
  });


  it("does not commit equal library polling snapshots", async () => {
    vi.useFakeTimers();
    const same = { ...library, scan_status: "idle", media_count: 3 };
    const coordinator = makeCoordinator({ fetchLibraries: vi.fn().mockResolvedValue({ ...same }) as never });
    coordinator.start([section]);
    await tick();
    expect(coordinator.handlers.onLibraries).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(HOME_LIBRARY_IDLE_POLL_MS);
    expect(coordinator.handlers.onLibraries).toHaveBeenCalledTimes(1);
    coordinator.stop();
  });
  it("can restart a generation without showing initial loading", async () => {
    const coordinator = makeCoordinator();
    coordinator.start([section], { showInitialLoading: false });
    await tick();
    expect(coordinator.handlers.onLoading).not.toHaveBeenCalledWith(true);
    coordinator.stop();
  });});
