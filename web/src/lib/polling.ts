export const LIBRARY_ACTIVE_POLL_MS = 3_000;
export const LIBRARY_IDLE_POLL_MS = 15_000;

export type PollController = {
  start(): void;
  stop(): void;
  refreshNow(): void;
};

export type GuardedPollOptions<T> = {
  load(signal: AbortSignal): Promise<T>;
  delay(value: T): number | null;
  equal(a: T, b: T): boolean;
  commit(value: T): void;
  onError?(error: unknown): void;
  errorDelay?: number | null | ((consecutiveFailures: number, error: unknown) => number | null);
};

export function createGuardedPoll<T>(options: GuardedPollOptions<T>): PollController {
  let generation = 0;
  let started = false;
  let dirty = false;
  let inFlight = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let controller: AbortController | undefined;
  let previous: T | undefined;
  let hasPrevious = false;
  let failures = 0;

  const clearTimer = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };

  const schedule = (delay: number | null, expectedGeneration: number) => {
    clearTimer();
    if (!started || generation !== expectedGeneration || delay == null) return;
    timer = setTimeout(() => {
      timer = undefined;
      void run(expectedGeneration);
    }, Math.max(0, delay));
  };

  const run = async (expectedGeneration: number): Promise<void> => {
    if (!started || generation !== expectedGeneration) return;
    if (inFlight) {
      dirty = true;
      return;
    }
    clearTimer();
    inFlight = true;
    const requestController = new AbortController();
    controller = requestController;
    let nextDelay: number | null = null;
    try {
      const value = await options.load(requestController.signal);
      if (!started || generation !== expectedGeneration || requestController.signal.aborted) return;
      failures = 0;
      if (!hasPrevious || !options.equal(previous as T, value)) {
        previous = value;
        hasPrevious = true;
        options.commit(value);
      }
      nextDelay = options.delay(value);
    } catch (error) {
      if (!started || generation !== expectedGeneration || requestController.signal.aborted) return;
      failures += 1;
      options.onError?.(error);
      const configured = options.errorDelay ?? ((count: number) => Math.min(30_000, 5_000 * 2 ** (count - 1)));
      nextDelay = typeof configured === "function" ? configured(failures, error) : configured;
    } finally {
      if (controller === requestController) controller = undefined;
      inFlight = false;
      if (!started || generation !== expectedGeneration || requestController.signal.aborted) return;
      if (dirty) {
        dirty = false;
        void run(expectedGeneration);
      } else {
        schedule(nextDelay, expectedGeneration);
      }
    }
  };

  return {
    start() {
      if (started) return;
      started = true;
      dirty = false;
      const expectedGeneration = ++generation;
      void run(expectedGeneration);
    },
    stop() {
      started = false;
      dirty = false;
      ++generation;
      clearTimer();
      controller?.abort();
      controller = undefined;
    },
    refreshNow() {
      if (!started) return;
      clearTimer();
      if (inFlight) {
        dirty = true;
        return;
      }
      void run(generation);
    },
  };
}

export function createResumeRefresh(refresh: () => void, cooldownMs = 250): () => void {
  let lastRefreshAt = Number.NEGATIVE_INFINITY;
  return () => {
    const now = Date.now();
    if (now - lastRefreshAt <= cooldownMs) return;
    lastRefreshAt = now;
    refresh();
  };
}
