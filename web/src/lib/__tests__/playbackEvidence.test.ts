import { afterEach, describe, expect, it, vi } from "vitest";
import { createPlaybackEvidenceReporter } from "../playbackEvidence";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("createPlaybackEvidenceReporter", () => {
  it("keeps one immutable session and orders every evidence event", () => {
    const reporter = createPlaybackEvidenceReporter(() => "session-a");

    expect(reporter.event("start", 0)).toEqual({
      session_id: "session-a", sequence: 1, event: "start", position: 0,
    });
    expect(reporter.event("seek", 500)).toEqual({
      session_id: "session-a", sequence: 2, event: "seek", position: 500,
    });
    expect(reporter.event("progress", 510)).toEqual({
      session_id: "session-a", sequence: 3, event: "progress", position: 510,
    });
    expect(reporter.event("ended", 600)).toEqual({
      session_id: "session-a", sequence: 4, event: "ended", position: 600,
    });
    expect(Object.keys(reporter)).toEqual(["event"]);
  });

  it("reads the current trimmed JIT ID without changing session or sequence", () => {
    let jit = " jit-a ";
    const reporter = createPlaybackEvidenceReporter(() => "app-session", () => jit);

    expect(reporter.event("start", 0)).toEqual({
      session_id: "app-session", jit_session_id: "jit-a", sequence: 1, event: "start", position: 0,
    });
    jit = "   ";
    expect(reporter.event("progress", 1)).toEqual({
      session_id: "app-session", sequence: 2, event: "progress", position: 1,
    });
    jit = "jit-b";
    expect(reporter.event("ended", 2)).toEqual({
      session_id: "app-session", jit_session_id: "jit-b", sequence: 3, event: "ended", position: 2,
    });
  });

  it("omits a throwing JIT getter and keeps returned events contiguous", () => {
    let throws = true;
    const reporter = createPlaybackEvidenceReporter(
      () => "app-session",
      () => {
        if (throws) throw new Error("JIT unavailable");
        return "jit-ok";
      },
    );

    expect(reporter.event("start", 0)).toEqual({
      session_id: "app-session", sequence: 1, event: "start", position: 0,
    });
    throws = false;
    expect(reporter.event("progress", 1)).toEqual({
      session_id: "app-session", jit_session_id: "jit-ok", sequence: 2, event: "progress", position: 1,
    });
  });

  it.each([null, 42, {}, "x".repeat(129), String.fromCharCode(0xd800)])("omits an invalid runtime JIT ID: %s", (jit) => {
    const reporter = createPlaybackEvidenceReporter(
      () => "app-session",
      () => jit as unknown as string,
    );
    expect(reporter.event("progress", 1)).not.toHaveProperty("jit_session_id");
  });

  it.each([
    [-1, 0], [-0, 0], [1.99, 1], [Number.NaN, 0],
    [Number.NEGATIVE_INFINITY, 0], [Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER],
    [Number.MAX_VALUE, Number.MAX_SAFE_INTEGER],
    [Number.MAX_SAFE_INTEGER + 100, Number.MAX_SAFE_INTEGER],
  ])("normalizes position %s to a safe non-negative integer", (input, expected) => {
    const position = createPlaybackEvidenceReporter(() => "session").event("progress", input).position;
    expect(position).toBe(expected);
    expect(Object.is(position, -0)).toBe(false);
    expect(Number.isSafeInteger(position)).toBe(true);
  });

  it.each(["", "   "])("rejects an empty factory ID", (id) => {
    expect(() => createPlaybackEvidenceReporter(() => id)).toThrow(/non-empty session ID/i);
  });

  it("accepts exactly 128 UTF-8 bytes and rejects 129", () => {
    expect(createPlaybackEvidenceReporter(() => "a".repeat(128)).event("start", 0).session_id)
      .toHaveLength(128);
    expect(() => createPlaybackEvidenceReporter(() => "a".repeat(129))).toThrow(/128 UTF-8 bytes/i);
  });

  it("validates multibyte session IDs by UTF-8 byte length", () => {
    expect(createPlaybackEvidenceReporter(() => "界".repeat(42) + "é").event("start", 0).session_id)
      .toBe("界".repeat(42) + "é");
    expect(() => createPlaybackEvidenceReporter(() => "界".repeat(43))).toThrow(/128 UTF-8 bytes/i);
  });

  it.each([String.fromCharCode(0xd800), String.fromCharCode(0xdc00), "ok" + String.fromCharCode(0xd800) + "bad", "ok" + String.fromCharCode(0xdc00) + "bad"])("rejects lone surrogate session IDs", (id) => {
    expect(() => createPlaybackEvidenceReporter(() => id)).toThrow(/Unicode scalar/i);
  });

  it("propagates ID factory errors", () => {
    const failure = new Error("uuid failed");
    expect(() => createPlaybackEvidenceReporter(() => { throw failure; })).toThrow(failure);
  });

  it("uses randomUUID by default and gives fresh reporters different IDs", () => {
    const randomUUID = vi.spyOn(globalThis.crypto, "randomUUID")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000001")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000002");

    const first = createPlaybackEvidenceReporter().event("start", 0);
    const second = createPlaybackEvidenceReporter().event("start", 0);

    expect(first.session_id).not.toBe(second.session_id);
    expect(randomUUID).toHaveBeenCalledTimes(2);
  });

  it("uses getRandomValues for valid distinct UUIDv4 IDs when randomUUID is absent", () => {
    let seed = 0;
    vi.stubGlobal("crypto", {
      getRandomValues: (bytes: Uint8Array) => {
        bytes.fill(seed++);
        return bytes;
      },
    });

    const first = createPlaybackEvidenceReporter().event("start", 0).session_id;
    const second = createPlaybackEvidenceReporter().event("start", 0).session_id;

    expect(first).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(second).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(first).not.toBe(second);
  });

  it("uses a distinct best-effort ID without crypto", () => {
    vi.stubGlobal("crypto", undefined);
    vi.spyOn(Date, "now").mockReturnValue(123456789);
    vi.spyOn(Math, "random").mockReturnValue(0.25);

    const first = createPlaybackEvidenceReporter().event("start", 0).session_id;
    const second = createPlaybackEvidenceReporter().event("start", 0).session_id;

    expect(first).toBeTruthy();
    expect(second).toBeTruthy();
    expect(first).not.toBe(second);
  });

  it("falls back to getRandomValues when randomUUID throws", () => {
    vi.stubGlobal("crypto", {
      randomUUID: () => { throw new Error("uuid unavailable"); },
      getRandomValues: (bytes: Uint8Array) => { bytes.fill(7); return bytes; },
    });
    expect(createPlaybackEvidenceReporter().event("start", 0).session_id)
      .toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });

  it("uses best-effort IDs when both crypto methods throw", () => {
    vi.stubGlobal("crypto", {
      randomUUID: () => { throw new Error("uuid unavailable"); },
      getRandomValues: () => { throw new Error("rng unavailable"); },
    });
    expect(createPlaybackEvidenceReporter().event("start", 0).session_id).toMatch(/^play-/);
  });

});