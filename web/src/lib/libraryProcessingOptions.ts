import type { Library } from "../api/client";

export const LIBRARY_PROCESSING_OPTION_NAMES = [
  "preview", "subtitle_extract", "atrack_extract", "subtitle_recognize", "keyframe_extract", "ai_analysis",
  // Phase 5 audio
  "lyric_recognize", "audio_analysis",
  // Phase 5 image
  "photo_classify", "photo_geocode", "photo_face", "image_ocr",
  // Phase 5 document
  "document_convert", "document_fulltext",
] as const;

export type LibraryProcessingOptionName = (typeof LIBRARY_PROCESSING_OPTION_NAMES)[number];
export type LibraryProcessingChoices = Record<LibraryProcessingOptionName, boolean>;
export type LibraryProcessingLockReason =
  | "subtitle_recognize"
  | "ai_analysis"
  | "photo_classify"
  | "photo_geocode"
  | "photo_face"
  | "image_ocr"
  | "document_fulltext"
  | "document_convert"
  | "audio_analysis"
  | "lyric_recognize";

export const EMPTY_LIBRARY_PROCESSING_CHOICES: LibraryProcessingChoices = {
  preview: false,
  subtitle_extract: false,
  atrack_extract: false,
  subtitle_recognize: false,
  keyframe_extract: false,
  ai_analysis: false,
  // Phase 5 audio
  lyric_recognize: false,
  audio_analysis: false,
  // Phase 5 image
  photo_classify: false,
  photo_geocode: false,
  photo_face: false,
  image_ocr: false,
  // Phase 5 document
  document_convert: false,
  document_fulltext: false,
};

export function deriveEffectiveProcessingOptions(
  explicit: LibraryProcessingChoices,
): LibraryProcessingChoices {
  const effective = { ...explicit };
  // Video: AI → subtitle_recognize → subtitle_extract + atrack_extract
  if (effective.ai_analysis) effective.subtitle_recognize = true;
  if (effective.subtitle_recognize) {
    effective.subtitle_extract = true;
    effective.atrack_extract = true;
  }
  // Audio: AI → lyric_recognize
  if (effective.ai_analysis) effective.lyric_recognize = true;
  // Image: AI → classify/geocode/face
  if (effective.ai_analysis) {
    effective.photo_classify = true;
    effective.photo_geocode = true;
    effective.photo_face = true;
  }
  // Document: AI → fulltext, OCR → fulltext, fulltext ↔ convert
  if (effective.ai_analysis) effective.document_fulltext = true;
  if (effective.image_ocr) effective.document_fulltext = true;
  if (effective.document_fulltext) effective.document_convert = true;
  if (effective.document_convert) effective.document_fulltext = true;
  return effective;
}

export function applyExplicitProcessingOptionChange(
  explicit: LibraryProcessingChoices,
  option: LibraryProcessingOptionName,
  enabled: boolean,
): LibraryProcessingChoices {
  const next = { ...explicit, [option]: enabled };
  if (!enabled && option === "ai_analysis") next.subtitle_recognize = true;
  if (!enabled && option === "subtitle_recognize") {
    next.subtitle_extract = true;
    next.atrack_extract = true;
  }
  // Phase 5 lock: unchecking prerequisite keeps dependents enabled
  if (!enabled && option === "ai_analysis") {
    next.lyric_recognize = true;
    next.photo_classify = true;
    next.photo_geocode = true;
    next.photo_face = true;
    next.document_fulltext = true;
  }
  if (!enabled && option === "document_fulltext") {
    next.document_convert = true;
  }
  if (!enabled && option === "image_ocr") {
    next.document_fulltext = true;
  }
  return next;
}

export function directProcessingOptionLockReason(
  choices: LibraryProcessingChoices,
  option: LibraryProcessingOptionName,
): LibraryProcessingLockReason | null {
  // Video
  if ((option === "subtitle_extract" || option === "atrack_extract") && choices.subtitle_recognize) {
    return "subtitle_recognize";
  }
  if (option === "subtitle_recognize" && choices.ai_analysis) return "ai_analysis";
  // Audio
  if (option === "lyric_recognize" && choices.ai_analysis) return "ai_analysis";
  if (option === "audio_analysis" && choices.ai_analysis) return "ai_analysis";
  // Image
  if (option === "photo_classify" && choices.ai_analysis) return "ai_analysis";
  if (option === "photo_geocode" && choices.ai_analysis) return "ai_analysis";
  if (option === "photo_face" && choices.ai_analysis) return "ai_analysis";
  // Document
  if (option === "document_fulltext" && choices.ai_analysis) return "ai_analysis";
  if (option === "document_fulltext" && choices.image_ocr) return "image_ocr";
  if (option === "document_convert" && choices.document_fulltext) return "document_fulltext";
  return null;
}

export function hydrateLibraryProcessingOptions(
  library?: Partial<Library> | null,
): LibraryProcessingChoices {
  return {
    preview: library?.preview_extract === 1,
    subtitle_extract: library?.subtitle_extract === 1,
    atrack_extract: library?.atrack_extract === 1,
    subtitle_recognize: library?.subtitle_recognize === 1,
    keyframe_extract: library?.keyframe_extract === 1,
    ai_analysis: library?.ai_analysis === 1,
    // Phase 5 audio
    lyric_recognize: (library as any)?.lyric_recognize === 1,
    audio_analysis: (library as any)?.audio_analysis === 1,
    // Phase 5 image
    photo_classify: (library as any)?.photo_classify === 1,
    photo_geocode: (library as any)?.photo_geocode === 1,
    photo_face: (library as any)?.photo_face === 1,
    image_ocr: (library as any)?.image_ocr === 1,
    // Phase 5 document
    document_convert: (library as any)?.document_convert === 1,
    document_fulltext: (library as any)?.document_fulltext === 1,
  };
}

export function processingOptionScalarFields(choices: LibraryProcessingChoices) {
  return {
    preview_extract: choices.preview ? 1 : 0,
    subtitle_extract: choices.subtitle_extract ? 1 : 0,
    atrack_extract: choices.atrack_extract ? 1 : 0,
    subtitle_recognize: choices.subtitle_recognize ? 1 : 0,
    keyframe_extract: choices.keyframe_extract ? 1 : 0,
    ai_analysis: choices.ai_analysis ? 1 : 0,
    // Phase 5 audio
    lyric_recognize: choices.lyric_recognize ? 1 : 0,
    audio_analysis: choices.audio_analysis ? 1 : 0,
    // Phase 5 image
    photo_classify: choices.photo_classify ? 1 : 0,
    photo_geocode: choices.photo_geocode ? 1 : 0,
    photo_face: choices.photo_face ? 1 : 0,
    image_ocr: choices.image_ocr ? 1 : 0,
    // Phase 5 document
    document_convert: choices.document_convert ? 1 : 0,
    document_fulltext: choices.document_fulltext ? 1 : 0,
  };
}

export function processingChoicesToForm(choices: LibraryProcessingChoices) {
  return {
    preview_extract: choices.preview,
    subtitle_extract: choices.subtitle_extract,
    atrack_extract: choices.atrack_extract,
    subtitle_recognize: choices.subtitle_recognize,
    keyframe_extract: choices.keyframe_extract,
    ai_analysis: choices.ai_analysis,
    // Phase 5 audio
    lyric_recognize: choices.lyric_recognize,
    audio_analysis: choices.audio_analysis,
    // Phase 5 image
    photo_classify: choices.photo_classify,
    photo_geocode: choices.photo_geocode,
    photo_face: choices.photo_face,
    image_ocr: choices.image_ocr,
    // Phase 5 document
    document_convert: choices.document_convert,
    document_fulltext: choices.document_fulltext,
  };
}

export function isVideoProcessingLibraryType(type?: string): boolean {
  return ["movie", "video", "tv", "anime"].includes((type || "").trim().toLowerCase());
}

export function isAudioProcessingLibraryType(type?: string): boolean {
  return ["music", "audio", "podcast"].includes((type || "").trim().toLowerCase());
}

export function isImageProcessingLibraryType(type?: string): boolean {
  return ["photo", "image", "picture"].includes((type || "").trim().toLowerCase());
}

export function isDocumentProcessingLibraryType(type?: string): boolean {
  return ["document", "book", "ebook"].includes((type || "").trim().toLowerCase());
}

export function mediaProcessingOptionNames(type?: string): LibraryProcessingOptionName[] {
  const t = (type || "").trim().toLowerCase();
  const names: LibraryProcessingOptionName[] = ["ai_analysis"];
  if (isVideoProcessingLibraryType(t)) {
    names.push("preview", "subtitle_extract", "atrack_extract", "subtitle_recognize", "keyframe_extract");
  }
  if (isAudioProcessingLibraryType(t)) {
    names.push("lyric_recognize", "audio_analysis");
  }
  if (isImageProcessingLibraryType(t)) {
    names.push("photo_classify", "photo_geocode", "photo_face", "image_ocr");
  }
  if (isDocumentProcessingLibraryType(t)) {
    names.push("document_convert", "document_fulltext");
  }
  return names;
}
