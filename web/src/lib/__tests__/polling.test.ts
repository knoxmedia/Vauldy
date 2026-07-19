import { afterEach, describe, expect, it, vi } from "vitest";
import { createGuardedPoll, createResumeRefresh } from "../polling";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}
const tick = async () => { await Promise.resolve(); await Promise.resolve(); };

afterEach(() => vi.useRealTimers());

describe("createGuardedPoll", () => {
  it("uses the completed value to schedule active then idle delays", async () => {
    vi.useFakeTimers();
    const load = vi.fn().mockResolvedValueOnce("running").mockResolvedValue("idle");
    const poll = createGuardedPoll({ load, delay: (v) => v === "running" ? 3_000 : 15_000, equal: Object.is, commit: vi.fn() });
    poll.start(); await tick();
    await vi.advanceTimersByTimeAsync(2_999); expect(load).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1); expect(load).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(14_999); expect(load).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1); expect(load).toHaveBeenCalledTimes(3);
    poll.stop();
  });

  it("never overlaps and coalesces refreshNow into one immediate dirty rerun", async () => {
    vi.useFakeTimers();
    const first = deferred<number>();
    const load = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(2);
    const poll = createGuardedPoll({ load, delay: () => 10_000, equal: Object.is, commit: vi.fn() });
    poll.start(); poll.refreshNow(); poll.refreshNow();
    expect(load).toHaveBeenCalledTimes(1);
    first.resolve(1); await tick();
    expect(load).toHaveBeenCalledTimes(2);
    poll.stop();
  });

  it("aborts on stop, drops stale generations, and start is idempotent", async () => {
    const request = deferred<number>();
    const commit = vi.fn();
    const load = vi.fn((_signal: AbortSignal) => request.promise);
    const poll = createGuardedPoll({ load, delay: () => null, equal: Object.is, commit });
    poll.start(); poll.start();
    expect(load).toHaveBeenCalledTimes(1);
    const signal = load.mock.calls[0]![0];
    poll.stop(); expect(signal.aborted).toBe(true);
    request.resolve(1); await tick();
    expect(commit).not.toHaveBeenCalled();
  });

  it("commits only changed values", async () => {
    vi.useFakeTimers();
    const commit = vi.fn();
    const poll = createGuardedPoll({ load: vi.fn().mockResolvedValue({ id: 1 }), delay: () => 1_000, equal: (a, b) => a.id === b.id, commit });
    poll.start(); await tick(); await vi.advanceTimersByTimeAsync(1_000);
    expect(commit).toHaveBeenCalledTimes(1);
    poll.stop();
  });

  it("backs off after errors and reports them without committing", async () => {
    vi.useFakeTimers();
    const onError = vi.fn();
    const load = vi.fn().mockRejectedValueOnce(new Error("boom")).mockResolvedValue(1);
    const poll = createGuardedPoll({ load, delay: () => 3_000, errorDelay: (failures) => failures * 5_000, equal: Object.is, commit: vi.fn(), onError });
    poll.start(); await tick();
    expect(onError).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(4_999); expect(load).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1); expect(load).toHaveBeenCalledTimes(2);
    poll.stop();
  });
});


describe("createResumeRefresh", () => {
  it("dispatches immediately, suppresses a paired wake event, and allows a later wake", () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000);
    const refresh = vi.fn();
    const resume = createResumeRefresh(refresh);
    resume();
    resume();
    expect(refresh).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(251);
    resume();
    expect(refresh).toHaveBeenCalledTimes(2);
  });
});
