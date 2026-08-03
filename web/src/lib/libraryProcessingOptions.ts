import type { Library } from "../api/client";

export const LIBRARY_PROCESSING_OPTION_NAMES = [
  "preview", "subtitle_extract", "atrack_extract", "subtitle_recognize", "keyframe_extract", "ai_analysis",
] as const;

export type LibraryProcessingOptionName = (typeof LIBRARY_PROCESSING_OPTION_NAMES)[number];
export type LibraryProcessingChoices = Record<LibraryProcessingOptionName, boolean>;
export type LibraryProcessingLockReason = "subtitle_recognize" | "ai_analysis";

export const EMPTY_LIBRARY_PROCESSING_CHOICES: LibraryProcessingChoices = {
  preview: false,
  subtitle_extract: false,
  atrack_extract: false,
  subtitle_recognize: false,
  keyframe_extract: false,
  ai_analysis: false,
};

export function deriveEffectiveProcessingOptions(
  explicit: LibraryProcessingChoices,
): LibraryProcessingChoices {
  const effective = { ...explicit };
  if (effective.ai_analysis) effective.subtitle_recognize = true;
  if (effective.subtitle_recognize) {
    effective.subtitle_extract = true;
    effective.atrack_extract = true;
  }
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
  return next;
}

export function directProcessingOptionLockReason(
  choices: LibraryProcessingChoices,
  option: LibraryProcessingOptionName,
): LibraryProcessingLockReason | null {
  if ((option === "subtitle_extract" || option === "atrack_extract") && choices.subtitle_recognize) {
    return "subtitle_recognize";
  }
  if (option === "subtitle_recognize" && choices.ai_analysis) return "ai_analysis";
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
  };
}

export function isVideoProcessingLibraryType(type?: string): boolean {
  return ["movie", "video", "tv", "anime"].includes((type || "").trim().toLowerCase());
}
