import { describe, expect, it } from "vitest";
import { isLikelyNaturalPlaybackEnd } from "../playbackComplete";

describe("isLikelyNaturalPlaybackEnd", () => {
  it("rejects events before playback starts or with no progress", () => {
    expect(isLikelyNaturalPlaybackEnd(false, 0, 120)).toBe(false);
    expect(isLikelyNaturalPlaybackEnd(true, 0, 120)).toBe(false);
  });

  it("accepts near-end progress when duration is known", () => {
    expect(isLikelyNaturalPlaybackEnd(true, 117, 120)).toBe(true);
    expect(isLikelyNaturalPlaybackEnd(true, 50, 120)).toBe(false);
  });

  it("accepts short clips near their duration", () => {
    expect(isLikelyNaturalPlaybackEnd(true, 2.6, 3)).toBe(true);
    expect(isLikelyNaturalPlaybackEnd(true, 1, 3)).toBe(false);
  });

  it("requires media ended when duration is unknown", () => {
    expect(isLikelyNaturalPlaybackEnd(true, 12, 0, false)).toBe(false);
    expect(isLikelyNaturalPlaybackEnd(true, 12, 0, true)).toBe(true);
  });
});
