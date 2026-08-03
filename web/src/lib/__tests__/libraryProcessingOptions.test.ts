import { describe, expect, it } from "vitest";
import {
  applyExplicitProcessingOptionChange,
  deriveEffectiveProcessingOptions,
  directProcessingOptionLockReason,
  hydrateLibraryProcessingOptions,
  isVideoProcessingLibraryType,
  processingOptionScalarFields,
  type LibraryProcessingChoices,
} from "../libraryProcessingOptions";

const off: LibraryProcessingChoices = {
  preview: false, subtitle_extract: false, atrack_extract: false,
  subtitle_recognize: false, keyframe_extract: false, ai_analysis: false,
};

describe("library processing options", () => {
  it("keeps AI enable explicit while deriving its complete effective closure", () => {
    const explicit = applyExplicitProcessingOptionChange(off, "ai_analysis", true);
    expect(explicit).toEqual({ ...off, ai_analysis: true });
    expect(deriveEffectiveProcessingOptions(explicit)).toEqual({
      ...off, ai_analysis: true, subtitle_recognize: true, subtitle_extract: true, atrack_extract: true,
    });
  });

  it("promotes recognition when AI is disabled without promoting extraction", () => {
    const explicit = applyExplicitProcessingOptionChange({ ...off, ai_analysis: true }, "ai_analysis", false);
    expect(explicit).toEqual({ ...off, subtitle_recognize: true });
    expect(deriveEffectiveProcessingOptions(explicit)).toEqual({
      ...off, subtitle_recognize: true, subtitle_extract: true, atrack_extract: true,
    });
  });

  it("promotes extraction choices when recognition is disabled", () => {
    const explicit = applyExplicitProcessingOptionChange({ ...off, subtitle_recognize: true }, "subtitle_recognize", false);
    expect(explicit).toEqual({ ...off, subtitle_extract: true, atrack_extract: true });
    expect(deriveEffectiveProcessingOptions(explicit)).toEqual({ ...off, subtitle_extract: true, atrack_extract: true });
  });

  it("derives direct lock reasons from effective dependencies only", () => {
    const effective = deriveEffectiveProcessingOptions({ ...off, ai_analysis: true });
    expect(directProcessingOptionLockReason(effective, "subtitle_extract")).toBe("subtitle_recognize");
    expect(directProcessingOptionLockReason(effective, "atrack_extract")).toBe("subtitle_recognize");
    expect(directProcessingOptionLockReason(effective, "subtitle_recognize")).toBe("ai_analysis");
    expect(directProcessingOptionLockReason(effective, "preview")).toBeNull();
  });

  it("hydrates explicit top-level scalars and serializes explicit intent only", () => {
    const explicit = hydrateLibraryProcessingOptions({
      preview_extract: 0, subtitle_extract: 0, atrack_extract: 0,
      subtitle_recognize: 0, keyframe_extract: 1, ai_analysis: 1,
      processing_options: {
        explicit: { ...off, keyframe_extract: true, ai_analysis: true },
        effective: { ...off, subtitle_extract: true, atrack_extract: true, subtitle_recognize: true, keyframe_extract: true, ai_analysis: true },
        provenance: { explicit: ["ai_analysis", "keyframe_extract"], dependency_added: ["atrack_extract", "subtitle_extract", "subtitle_recognize"] },
      },
    });
    expect(explicit).toEqual({ ...off, keyframe_extract: true, ai_analysis: true });
    expect(processingOptionScalarFields(explicit)).toEqual({
      preview_extract: 0, subtitle_extract: 0, atrack_extract: 0,
      subtitle_recognize: 0, keyframe_extract: 1, ai_analysis: 1,
    });
  });

  it("shows processing controls for every backend video-capable type", () => {
    for (const type of ["movie", "video", "tv", "anime"]) expect(isVideoProcessingLibraryType(type)).toBe(true);
    for (const type of ["music", "photo", "document", ""]) expect(isVideoProcessingLibraryType(type)).toBe(false);
  });
});
