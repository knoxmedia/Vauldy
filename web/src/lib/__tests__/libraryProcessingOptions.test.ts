import { describe, expect, it } from "vitest";
import {
  applyExplicitProcessingOptionChange,
  deriveEffectiveProcessingOptions,
  directProcessingOptionLockReason,
  hydrateLibraryProcessingOptions,
  isVideoProcessingLibraryType,
  isAudioProcessingLibraryType,
  isImageProcessingLibraryType,
  isDocumentProcessingLibraryType,
  mediaProcessingOptionNames,
  processingOptionScalarFields,
  type LibraryProcessingChoices,
} from "../libraryProcessingOptions";

const off: LibraryProcessingChoices = {
  preview: false, subtitle_extract: false, atrack_extract: false,
  subtitle_recognize: false, keyframe_extract: false, ai_analysis: false,
  lyric_recognize: false, audio_analysis: false,
  photo_classify: false, photo_geocode: false, photo_face: false, image_ocr: false,
  document_convert: false, document_fulltext: false,
};

describe("library processing options", () => {
  it("keeps AI enable explicit while deriving its complete effective closure", () => {
    const explicit = applyExplicitProcessingOptionChange(off, "ai_analysis", true);
    expect(explicit).toEqual({ ...off, ai_analysis: true });
    expect(deriveEffectiveProcessingOptions(explicit)).toEqual({
      ...off, ai_analysis: true, subtitle_recognize: true, subtitle_extract: true, atrack_extract: true,
      lyric_recognize: true,
      photo_classify: true, photo_geocode: true, photo_face: true,
      document_convert: true, document_fulltext: true,
    });
  });

  it("promotes recognition when AI is disabled without promoting extraction", () => {
    const explicit = applyExplicitProcessingOptionChange({ ...off, ai_analysis: true }, "ai_analysis", false);
    expect(explicit).toEqual({
      ...off, subtitle_recognize: true,
      lyric_recognize: true,
      photo_classify: true, photo_geocode: true, photo_face: true,
      document_fulltext: true,
    });
    expect(deriveEffectiveProcessingOptions(explicit)).toEqual({
      ...off, subtitle_recognize: true, subtitle_extract: true, atrack_extract: true,
      lyric_recognize: true,
      photo_classify: true, photo_geocode: true, photo_face: true,
      document_convert: true, document_fulltext: true,
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
    // Phase 5 lock reasons
    expect(directProcessingOptionLockReason(effective, "lyric_recognize")).toBe("ai_analysis");
    expect(directProcessingOptionLockReason(effective, "audio_analysis")).toBe("ai_analysis");
    expect(directProcessingOptionLockReason(effective, "photo_classify")).toBe("ai_analysis");
    expect(directProcessingOptionLockReason(effective, "photo_geocode")).toBe("ai_analysis");
    expect(directProcessingOptionLockReason(effective, "photo_face")).toBe("ai_analysis");
    expect(directProcessingOptionLockReason(effective, "document_fulltext")).toBe("ai_analysis");
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
      lyric_recognize: 0, audio_analysis: 0,
      photo_classify: 0, photo_geocode: 0, photo_face: 0, image_ocr: 0,
      document_convert: 0, document_fulltext: 0,
    });
  });

  it("shows processing controls for every backend video-capable type", () => {
    for (const type of ["movie", "video", "tv", "anime"]) expect(isVideoProcessingLibraryType(type)).toBe(true);
    for (const type of ["music", "photo", "document", ""]) expect(isVideoProcessingLibraryType(type)).toBe(false);
  });

  // Phase 5 media-typed policy tests
  it("audio AI enables lyric and analysis", () => {
    const ai = deriveEffectiveProcessingOptions({ ...off, ai_analysis: true });
    expect(ai.lyric_recognize).toBe(true);
    const audioWithAI = deriveEffectiveProcessingOptions({ ...off, audio_analysis: true, ai_analysis: true });
    expect(audioWithAI.audio_analysis).toBe(true);
    expect(audioWithAI.lyric_recognize).toBe(true);
  });

  it("image AI enables classify/geocode/face", () => {
    const ai = deriveEffectiveProcessingOptions({ ...off, ai_analysis: true });
    expect(ai.photo_classify).toBe(true);
    expect(ai.photo_geocode).toBe(true);
    expect(ai.photo_face).toBe(true);
    // Independent image options don't cascade
    const classify = deriveEffectiveProcessingOptions({ ...off, photo_classify: true });
    expect(classify.photo_geocode).toBe(false);
  });

  it("document AI and OCR enable fulltext which enables convert", () => {
    // AI → fulltext → convert
    const ai = deriveEffectiveProcessingOptions({ ...off, ai_analysis: true });
    expect(ai.document_fulltext).toBe(true);
    expect(ai.document_convert).toBe(true);
    // OCR → fulltext → convert
    const ocr = deriveEffectiveProcessingOptions({ ...off, image_ocr: true });
    expect(ocr.document_fulltext).toBe(true);
    expect(ocr.document_convert).toBe(true);
    // Convert ↔ fulltext bidirectional
    const convert = deriveEffectiveProcessingOptions({ ...off, document_convert: true });
    expect(convert.document_fulltext).toBe(true);
    expect(convert.document_convert).toBe(true);
  });

  it("no cross-media leakage", () => {
    // Audio only should not pull image/document
    const audio = deriveEffectiveProcessingOptions({ ...off, lyric_recognize: true });
    expect(audio.photo_classify).toBe(false);
    expect(audio.document_convert).toBe(false);
    // Image only should not pull audio/document
    const image = deriveEffectiveProcessingOptions({ ...off, photo_classify: true });
    expect(image.lyric_recognize).toBe(false);
    expect(image.document_convert).toBe(false);
    // Document only should not pull audio/image
    const doc = deriveEffectiveProcessingOptions({ ...off, document_convert: true });
    expect(doc.lyric_recognize).toBe(false);
    expect(doc.photo_classify).toBe(false);
  });

  it("media processing option names filters by type", () => {
    expect(mediaProcessingOptionNames("movie")).toContain("preview");
    expect(mediaProcessingOptionNames("movie")).toContain("subtitle_recognize");
    expect(mediaProcessingOptionNames("movie")).not.toContain("lyric_recognize");
    expect(mediaProcessingOptionNames("music")).toContain("lyric_recognize");
    expect(mediaProcessingOptionNames("music")).toContain("audio_analysis");
    expect(mediaProcessingOptionNames("photo")).toContain("photo_classify");
    expect(mediaProcessingOptionNames("photo")).toContain("photo_geocode");
    expect(mediaProcessingOptionNames("document")).toContain("document_convert");
    expect(mediaProcessingOptionNames("document")).toContain("document_fulltext");
  });

  it("isAudioProcessingLibraryType", () => {
    for (const type of ["music", "audio", "podcast"]) expect(isAudioProcessingLibraryType(type)).toBe(true);
    for (const type of ["movie", "photo", "document", ""]) expect(isAudioProcessingLibraryType(type)).toBe(false);
  });

  it("isImageProcessingLibraryType", () => {
    for (const type of ["photo", "image", "picture"]) expect(isImageProcessingLibraryType(type)).toBe(true);
    for (const type of ["movie", "music", "document", ""]) expect(isImageProcessingLibraryType(type)).toBe(false);
  });

  it("isDocumentProcessingLibraryType", () => {
    for (const type of ["document", "book", "ebook"]) expect(isDocumentProcessingLibraryType(type)).toBe(true);
    for (const type of ["movie", "music", "photo", ""]) expect(isDocumentProcessingLibraryType(type)).toBe(false);
  });
});
