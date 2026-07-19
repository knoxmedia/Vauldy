import type { HistoryItem, Library, MediaItem } from "../api/client";
import type { HomeRecentSection } from "./homeRecentSections";
import { createResumeRefresh, LIBRARY_ACTIVE_POLL_MS, LIBRARY_IDLE_POLL_MS } from "./polling";

export const HOME_LIBRARY_ACTIVE_POLL_MS = LIBRARY_ACTIVE_POLL_MS;
export const HOME_LIBRARY_IDLE_POLL_MS = LIBRARY_IDLE_POLL_MS;
export const HOME_RECENT_POLL_MS = 10_000;

export type HomeRequestDependencies = {
  fetchLibraries: (signal?: AbortSignal) => Promise<Library[]>;
  fetchHistory: (signal?: AbortSignal) => Promise<HistoryItem[]>;
  loadRecent: (libs: Library[], sections: HomeRecentSection[], signal: AbortSignal, onSection: (key: string, items: MediaItem[]) => void) => Promise<void>;
};
export type HomeRequestHandlers = {
  onLoading: (loading: boolean) => void;
  onLibraries: (items: Library[]) => void;
  onHistory: (items: HistoryItem[]) => void;
  onSection: (key: string, items: MediaItem[]) => void;
};

function recentLibraryShape(libs: Library[]): string {
  return libs.map(({ id, type }) => `${id}:${type}`).sort().join("|");
}

function hasActiveScan(libs: Library[]): boolean {
  return libs.some((lib) => ["running", "scanning"].includes((lib.scan_status || "").trim().toLowerCase()));
}

function stableLibraryProjection(libs: Library[]): string {
  return JSON.stringify(libs.map((lib) => ({
    id: lib.id, name: lib.name, type: lib.type, path: lib.path, folders: lib.folders ?? [],
    auto_scan: lib.auto_scan, enabled: lib.enabled, realtime_monitor: lib.realtime_monitor,
    preview_extract: lib.preview_extract, drm_enabled: lib.drm_enabled, encryption_mode: lib.encryption_mode,
    encrypted_assets_enabled: lib.encrypted_assets_enabled,
    encrypted_assets_cleanup_plaintext: lib.encrypted_assets_cleanup_plaintext,
    encrypted_assets_dir_mode: lib.encrypted_assets_dir_mode,
    encrypted_assets_custom_dir: lib.encrypted_assets_custom_dir,
    metadata_providers: lib.metadata_providers ?? [], image_providers: lib.image_providers ?? [],
    metadata_refresh_policy: lib.metadata_refresh_policy, scraper: lib.scraper, created_at: lib.created_at,
    media_count: lib.media_count, scan_task_id: lib.scan_task_id, scan_status: lib.scan_status,
    scan_processed_count: lib.scan_processed_count, scan_total_count: lib.scan_total_count,
    scan_added_count: lib.scan_added_count, scan_started_at: lib.scan_started_at, preview_url: lib.preview_url,
  })));
}

export class HomeRequestCoordinator {
  private generation = 0;
  private dataEpoch = 0;
  private sections: HomeRecentSection[] = [];
  private libraries: Library[] = [];
  private initController?: AbortController;
  private libraryController?: AbortController;
  private recentController?: AbortController;
  private mutationController?: AbortController;
  private initLibraryInFlight = false;
  private libraryInFlight = false;
  private libraryRefreshDirty = false;
  private recentRefreshDirty = false;
  private mutationDirty = false;
  private mutationPromise?: Promise<void>;
  private currentLibraryPromise?: Promise<void>;
  private currentHistoryPromise?: Promise<void>;
  private currentRecentPromise?: Promise<void>;
  private libraryTimer?: number;
  private recentTimer?: number;
  private loadingVisible = false;
  private libraryFailures = 0;
  private recentFailures = 0;

  constructor(public readonly dependencies: HomeRequestDependencies, public readonly handlers: HomeRequestHandlers) {}

  start(sections: HomeRecentSection[], options: { showInitialLoading?: boolean } = {}): void {
    this.stop();
    const generation = ++this.generation;
    const epoch = this.dataEpoch;
    this.sections = sections;
    this.libraries = [];
    this.loadingVisible = options.showInitialLoading ?? true;
    this.libraryFailures = 0;
    this.recentFailures = 0;
    if (this.loadingVisible) this.handlers.onLoading(true);
    const controller = new AbortController();
    this.initController = controller;
    let basePending = 2;
    const baseSettled = () => {
      basePending -= 1;
      if (basePending === 0) this.reveal(generation, controller.signal);
    };

    this.initLibraryInFlight = true;
    const libraryPromise = this.dependencies.fetchLibraries(controller.signal).then((items) => {
      if (!this.active(generation, controller.signal)) return;
      this.libraryFailures = 0;
      this.libraries = Array.isArray(items) ? items : [];
      this.handlers.onLibraries(this.libraries);
      this.reveal(generation, controller.signal);
      if (this.libraries.length > 0) void this.refreshRecent(generation, true, epoch);
    }).catch(() => { this.libraryFailures += 1; }).finally(() => {
      if (this.initController === controller) this.initLibraryInFlight = false;
      if (this.currentLibraryPromise === libraryPromise) this.currentLibraryPromise = undefined;
      baseSettled();
      if (this.active(generation, controller.signal)) {
        if (this.libraryRefreshDirty) {
          this.libraryRefreshDirty = false;
          void this.pollLibraries(generation);
        } else {
          this.scheduleLibrary(generation);
        }
      }
    });
    this.currentLibraryPromise = libraryPromise;

    const historyPromise = this.dependencies.fetchHistory(controller.signal).then((items) => {
      if (!this.activeData(generation, epoch, controller.signal)) return;
      this.handlers.onHistory(items.filter((item) => item.media_id > 0));
      this.reveal(generation, controller.signal);
    }).catch(() => undefined).finally(() => {
      if (this.currentHistoryPromise === historyPromise) this.currentHistoryPromise = undefined;
      baseSettled();
    });
    this.currentHistoryPromise = historyPromise;
    document.addEventListener("visibilitychange", this.onVisibilityChange);
    window.addEventListener("focus", this.onFocus);
  }

  refreshAfterMutation(): Promise<void> {
    if (this.mutationPromise) {
      this.mutationDirty = true;
      return this.mutationPromise;
    }
    const generation = this.generation;
    const run = async () => {
      do {
        this.mutationDirty = false;
        const epoch = ++this.dataEpoch;
        const preexisting = [this.currentLibraryPromise, this.currentHistoryPromise, this.currentRecentPromise]
          .filter((promise): promise is Promise<void> => Boolean(promise));
        await Promise.allSettled(preexisting);
        if (!this.active(generation)) return;
        const controller = new AbortController();
        this.mutationController = controller;
        await Promise.allSettled([
          this.startHistoryRequest(generation, epoch, controller),
          this.startRecentRequest(generation, epoch),
        ]);
      } while (this.mutationDirty && this.active(generation));
    };
    const promise = run().finally(() => {
      if (this.mutationPromise === promise) this.mutationPromise = undefined;
    });
    this.mutationPromise = promise;
    return promise;
  }

  stop(): void {
    ++this.generation;
    ++this.dataEpoch;
    this.initController?.abort();
    this.libraryController?.abort();
    this.recentController?.abort();
    this.mutationController?.abort();
    this.initLibraryInFlight = false;
    this.libraryInFlight = false;
    this.libraryRefreshDirty = false;
    this.recentRefreshDirty = false;
    this.mutationDirty = false;
    this.mutationPromise = undefined;
    this.currentLibraryPromise = undefined;
    this.currentHistoryPromise = undefined;
    this.currentRecentPromise = undefined;
    this.loadingVisible = false;
    this.clearLibraryTimer();
    this.clearRecentTimer();
    document.removeEventListener("visibilitychange", this.onVisibilityChange);
    window.removeEventListener("focus", this.onFocus);
  }

  private active(generation: number, signal?: AbortSignal): boolean {
    return generation === this.generation && !signal?.aborted;
  }

  private activeData(generation: number, epoch: number, signal?: AbortSignal): boolean {
    return this.active(generation, signal) && epoch === this.dataEpoch;
  }

  private reveal(generation: number, signal?: AbortSignal): void {
    if (!this.loadingVisible || !this.active(generation, signal)) return;
    this.loadingVisible = false;
    this.handlers.onLoading(false);
  }

  private async startHistoryRequest(generation: number, epoch: number, controller: AbortController): Promise<void> {
    if (!this.active(generation, controller.signal)) return;
    const promise = this.dependencies.fetchHistory(controller.signal).then((items) => {
      if (this.activeData(generation, epoch, controller.signal)) {
        this.handlers.onHistory(items.filter((item) => item.media_id > 0));
      }
    }).catch(() => undefined).finally(() => {
      if (this.currentHistoryPromise === promise) this.currentHistoryPromise = undefined;
    });
    this.currentHistoryPromise = promise;
    await promise;
  }

  private async startRecentRequest(generation: number, epoch: number): Promise<void> {
    if (this.currentRecentPromise || this.libraries.length === 0 || !this.active(generation) || document.hidden) {
      await this.currentRecentPromise;
      return;
    }
    const controller = new AbortController();
    this.recentController = controller;
    const promise = this.dependencies.loadRecent(this.libraries, this.sections, controller.signal, (key, items) => {
      if (!this.activeData(generation, epoch, controller.signal)) return;
      this.handlers.onSection(key, items);
      this.reveal(generation, controller.signal);
    }).then(() => { this.recentFailures = 0; }).catch(() => { this.recentFailures += 1; }).finally(() => {
      if (this.currentRecentPromise === promise) this.currentRecentPromise = undefined;
      if (this.active(generation, controller.signal)) {
        if (this.recentRefreshDirty) {
          this.recentRefreshDirty = false;
          void this.refreshRecent(generation);
        } else {
          this.scheduleRecent(generation);
        }
      }
    });
    this.currentRecentPromise = promise;
    await promise;
  }

  private clearLibraryTimer(): void {
    if (this.libraryTimer !== undefined) window.clearTimeout(this.libraryTimer);
    this.libraryTimer = undefined;
  }

  private clearRecentTimer(): void {
    if (this.recentTimer !== undefined) window.clearTimeout(this.recentTimer);
    this.recentTimer = undefined;
  }

  private scheduleLibrary(generation: number): void {
    this.clearLibraryTimer();
    if (!this.active(generation) || document.hidden) return;
    const base = hasActiveScan(this.libraries) ? HOME_LIBRARY_ACTIVE_POLL_MS : HOME_LIBRARY_IDLE_POLL_MS;
    const delay = Math.min(60_000, base * 2 ** this.libraryFailures);
    this.libraryTimer = window.setTimeout(() => void this.pollLibraries(generation), delay);
  }

  private scheduleRecent(generation: number): void {
    this.clearRecentTimer();
    if (!this.active(generation) || document.hidden || this.libraries.length === 0) return;
    const delay = Math.min(60_000, HOME_RECENT_POLL_MS * 2 ** this.recentFailures);
    this.recentTimer = window.setTimeout(() => void this.refreshRecent(generation), delay);
  }

  private resumeRefresh = createResumeRefresh(() => {
    if (document.hidden) return;
    const generation = this.generation;
    this.requestLibraryRefresh(generation);
    this.requestRecentRefresh(generation);
  });

  private onFocus = (): void => this.resumeRefresh();

  private requestRecentRefresh(generation: number): void {
    this.clearRecentTimer();
    if (this.currentRecentPromise) {
      this.recentRefreshDirty = true;
      return;
    }
    void this.refreshRecent(generation);
  }

  private requestLibraryRefresh(generation: number): void {
    this.clearLibraryTimer();
    if (this.initLibraryInFlight || this.libraryInFlight) {
      this.libraryRefreshDirty = true;
      return;
    }
    void this.pollLibraries(generation);
  }

  private onVisibilityChange = (): void => {
    if (document.hidden) {
      this.clearLibraryTimer();
      this.clearRecentTimer();
      return;
    }
    this.resumeRefresh();
  };

  private async pollLibraries(generation: number): Promise<void> {
    this.clearLibraryTimer();
    if (this.initLibraryInFlight || this.libraryInFlight) {
      this.libraryRefreshDirty = true;
      return;
    }
    if (!this.active(generation) || document.hidden) return;
    this.libraryInFlight = true;
    const controller = new AbortController();
    this.libraryController = controller;
    const promise = this.dependencies.fetchLibraries(controller.signal).then(async (items) => {
      if (!this.active(generation, controller.signal)) return;
      this.libraryFailures = 0;
      const next = Array.isArray(items) ? items : [];
      const recentChanged = recentLibraryShape(next) !== recentLibraryShape(this.libraries);
      const renderingChanged = stableLibraryProjection(next) !== stableLibraryProjection(this.libraries);
      this.libraries = next;
      if (renderingChanged) this.handlers.onLibraries(next);
      if (recentChanged) {
        this.recentController?.abort();
        this.currentRecentPromise = undefined;
        await this.refreshRecent(generation, true);
      }
    }).catch(() => { this.libraryFailures += 1; }).finally(() => {
      if (this.currentLibraryPromise === promise) this.currentLibraryPromise = undefined;
      if (this.libraryController === controller) this.libraryInFlight = false;
      if (this.active(generation, controller.signal)) {
        if (this.libraryRefreshDirty) {
          this.libraryRefreshDirty = false;
          void this.pollLibraries(generation);
        } else {
          this.scheduleLibrary(generation);
        }
      }
    });
    this.currentLibraryPromise = promise;
    await promise;
  }

  private async refreshRecent(generation: number, initial = false, epoch = this.dataEpoch): Promise<void> {
    if (!initial) this.clearRecentTimer();
    await this.startRecentRequest(generation, epoch);
  }
}
