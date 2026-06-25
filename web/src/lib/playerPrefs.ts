import type { MediaSubtitleRow } from "../api/client";
import type { SubtitleAppearance } from "./subtitleAppearance";
import { defaultSubtitleAppearance, normalizeSubtitleAppearance } from "./subtitleAppearance";

export type PlayerPrefs = {
  auto_select: boolean;
  preferred_audio_lang: string;
  preferred_subtitle_lang: string;
  subtitle_mode: "foreign" | "always" | "off";
  sdh_search: "prefer_non_sdh" | "prefer_sdh";
  forced_search: "prefer_non_forced" | "prefer_forced";
  subtitle_appearance?: SubtitleAppearance;
};

export function defaultPlayerPrefs(): PlayerPrefs {
  return {
    auto_select: true,
    preferred_audio_lang: "",
    preferred_subtitle_lang: "",
    subtitle_mode: "foreign",
    sdh_search: "prefer_non_sdh",
    forced_search: "prefer_non_forced",
    subtitle_appearance: defaultSubtitleAppearance(),
  };
}

export function normalizePlayerPrefs(raw: Partial<PlayerPrefs> | null | undefined): PlayerPrefs {
  const d = defaultPlayerPrefs();
  if (!raw || typeof raw !== "object") return d;
  return {
    auto_select: raw.auto_select !== false,
    preferred_audio_lang: typeof raw.preferred_audio_lang === "string" ? raw.preferred_audio_lang : d.preferred_audio_lang,
    preferred_subtitle_lang:
      typeof raw.preferred_subtitle_lang === "string" ? raw.preferred_subtitle_lang : d.preferred_subtitle_lang,
    subtitle_mode:
      raw.subtitle_mode === "always" || raw.subtitle_mode === "off" || raw.subtitle_mode === "foreign"
        ? raw.subtitle_mode
        : d.subtitle_mode,
    sdh_search: raw.sdh_search === "prefer_sdh" ? "prefer_sdh" : "prefer_non_sdh",
    forced_search: raw.forced_search === "prefer_forced" ? "prefer_forced" : "prefer_non_forced",
    subtitle_appearance: normalizeSubtitleAppearance(raw.subtitle_appearance),
  };
}

function langScore(pref: string, rowLang: string): number {
  const p = (pref || "").toLowerCase().trim();
  const r = (rowLang || "und").toLowerCase().trim();
  if (!p) return 1;
  if (!r || r === "und") return 0;
  if (r === p) return 100;
  if (r.startsWith(p) || p.startsWith(r)) return 85;
  if (p === "zh" && (r.startsWith("zh") || r === "chi")) return 100;
  if (p === "en" && r.startsWith("en")) return 100;
  return 0;
}

function labelBlob(r: MediaSubtitleRow): string {
  return `${r.label || ""} ${r.lang || ""}`.toLowerCase();
}

function scoreSubtitleRow(r: MediaSubtitleRow, prefs: PlayerPrefs): number {
  let s = langScore(prefs.preferred_subtitle_lang, r.lang);
  const lbl = labelBlob(r);
  if (prefs.sdh_search === "prefer_non_sdh" && /sdh|ccd|hearing impaired|聋|听障/.test(lbl)) s -= 35;
  if (prefs.sdh_search === "prefer_sdh" && /sdh|ccd/.test(lbl)) s += 20;
  if (prefs.forced_search === "prefer_non_forced" && /forced|强制/.test(lbl)) s -= 30;
  if (prefs.forced_search === "prefer_forced" && /forced|强制/.test(lbl)) s += 15;
  return s;
}

export type TextTrackItem = {
  id: string;
  language: string;
  text: string;
  url: string;
  isDefault: boolean;
};

export type PowerPlayerSubtitleTrack = {
  src: string;
  srclang: string;
  label: string;
  default: boolean;
};

function absoluteMediaUrl(relativeOrAbsolute: string, baseOrigin: string): string {
  if (!relativeOrAbsolute) return relativeOrAbsolute;
  if (/^https?:\/\//i.test(relativeOrAbsolute)) return relativeOrAbsolute;
  try {
    return new URL(relativeOrAbsolute, baseOrigin || "http://localhost").href;
  } catch {
    return relativeOrAbsolute;
  }
}

/** PowerPlayer 6 `subtitle: [{ src, srclang, label, default }]` */
export function buildPowerPlayerSubtitleList(
  tracks: TextTrackItem[],
  baseOrigin = typeof window !== "undefined" ? window.location.origin : ""
): PowerPlayerSubtitleTrack[] {
  return tracks.map((item) => {
    let src = absoluteMediaUrl(item.url, baseOrigin);
    if (!src.includes("format=")) {
      src += `${src.includes("?") ? "&" : "?"}format=powerplayer`;
    }
    return {
      src,
      srclang: item.language || "und",
      label: item.text,
      default: item.isDefault,
    };
  });
}

export function buildTextTrackListWithPrefs(
  mediaId: number,
  token: string,
  rows: MediaSubtitleRow[],
  prefsIn: PlayerPrefs | null | undefined
): { list: TextTrackItem[]; isDefaultOpen: boolean } {
  const prefs = normalizePlayerPrefs(prefsIn);
  const ready = rows.filter((r) => r.status === "ready");

  const langLabel = (c: string) => {
    const map: Record<string, string> = {
      zh: "中文",
      en: "English",
      ja: "日本語",
      ko: "한국어",
      fr: "Français",
      de: "Deutsch",
      es: "Español",
      ru: "Русский",
      pt: "Português",
      it: "Italiano",
      und: "未知语言",
    };
    return map[c] || (c ? c : "未知语言");
  };
  const kindLabel = (kind: string) => {
    switch (kind) {
      case "embedded":
        return "内嵌";
      case "external":
        return "外挂";
      case "asr":
        return "识别";
      default:
        return kind || "—";
    }
  };

  const list: TextTrackItem[] = ready.map((r) => {
    const lang = langLabel(r.lang);
    const k = kindLabel(r.source_kind);
    const extra = r.label ? ` · ${r.label}` : "";
    return {
      id: String(r.id),
      language: r.lang || "und",
      text: `${lang}（${k}）${extra}`,
      url: `/api/v1/media/${mediaId}/subtitles/${r.id}/vtt?access_token=${encodeURIComponent(token)}`,
      isDefault: false,
    };
  });

  if (!prefs.auto_select || prefs.subtitle_mode === "off" || list.length === 0) {
    return { list, isDefaultOpen: false };
  }
  const prefSub = prefs.preferred_subtitle_lang.trim();
  if (!prefSub) {
    return { list, isDefaultOpen: false };
  }
  let bestIdx = -1;
  let bestScore = -1;
  ready.forEach((r, i) => {
    const sc = scoreSubtitleRow(r, prefs);
    if (sc > bestScore) {
      bestScore = sc;
      bestIdx = i;
    }
  });

  const minScoreToUse = 20;
  if (bestIdx >= 0 && bestScore >= minScoreToUse) {
    list[bestIdx] = { ...list[bestIdx]!, isDefault: true };
    const open = prefs.subtitle_mode === "always" || prefs.subtitle_mode === "foreign";
    return { list, isDefaultOpen: open };
  }

  return { list, isDefaultOpen: false };
}

const SUBTITLE_MODE_LABEL: Record<PlayerPrefs["subtitle_mode"], string> = {
  foreign: "以外语音频显示",
  always: "始终显示",
  off: "关闭",
};

const SDH_LABEL: Record<PlayerPrefs["sdh_search"], string> = {
  prefer_non_sdh: "首选非SDH字幕",
  prefer_sdh: "首选SDH字幕",
};

const FORCED_LABEL: Record<PlayerPrefs["forced_search"], string> = {
  prefer_non_forced: "非强制字幕优先",
  prefer_forced: "强制字幕优先",
};

export function summarizePlayerPrefs(prefsIn: PlayerPrefs | null | undefined): string {
  const p = normalizePlayerPrefs(prefsIn);
  const auto = p.auto_select ? "自动选择 — " : "手动选择 — ";
  const track = `曲目选择 — ${SUBTITLE_MODE_LABEL[p.subtitle_mode]}`;
  const search = `搜索 — ${SDH_LABEL[p.sdh_search]}，${FORCED_LABEL[p.forced_search]}`;
  return `${auto}${track}\n${search}`;
}
