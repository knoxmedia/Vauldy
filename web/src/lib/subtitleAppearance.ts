/** 外挂 VTT 字幕外观（xgplayer TextTrack / xgplayer-subtitles + Knox CSS 变量） */

import type { CSSProperties } from "react";
import type { TranslateFn } from "../i18n";
export type SubtitleTextSize = "small" | "normal" | "large" | "xlarge";

export type SubtitleAppearance = {
  text_size: SubtitleTextSize;
  /** 预设键：white | black | yellow | cyan | green */
  text_color: string;
  /** none：无描边；shadow：轻投影；strong：重投影 */
  shadow: "none" | "shadow" | "strong";
  /** 预设键：blue | black | white | yellow | transparent */
  bg_color: string;
  /** 0–100，透明背景时忽略 */
  bg_opacity: number;
  /** 距画面底部百分比 0–30 */
  pos_bottom: number;
  /** 距画面顶部限制百分比 0–30（预留，播放器侧主要用 pos_bottom） */
  pos_top: number;
};

const TEXT_SIZE_VALUES: SubtitleTextSize[] = ["small", "normal", "large", "xlarge"];
const TEXT_COLOR_VALUES = ["white", "black", "yellow", "cyan", "green"] as const;
const SHADOW_VALUES: SubtitleAppearance["shadow"][] = ["none", "shadow", "strong"];
const BG_COLOR_VALUES = ["blue", "black", "white", "yellow", "transparent"] as const;

export function buildTextSizeOptions(t: TranslateFn): { value: SubtitleTextSize; label: string }[] {
  return TEXT_SIZE_VALUES.map((value) => ({
    value,
    label: t(`settings.subtitle_appearance.options.text_size.${value}`),
  }));
}

export function buildTextColorOptions(t: TranslateFn): { value: string; label: string }[] {
  return TEXT_COLOR_VALUES.map((value) => ({
    value,
    label: t(`settings.subtitle_appearance.options.text_color.${value}`),
  }));
}

export function buildShadowOptions(t: TranslateFn): { value: SubtitleAppearance["shadow"]; label: string }[] {
  return SHADOW_VALUES.map((value) => ({
    value,
    label: t(`settings.subtitle_appearance.options.shadow.${value}`),
  }));
}

export function buildBgColorOptions(t: TranslateFn): { value: string; label: string }[] {
  return BG_COLOR_VALUES.map((value) => ({
    value,
    label: t(`settings.subtitle_appearance.options.bg_color.${value}`),
  }));
}

function optionLabel(
  options: { value: string; label: string }[],
  value: string,
  fallbackKey: string,
  t: TranslateFn,
): string {
  return options.find((o) => o.value === value)?.label ?? t(fallbackKey);
}

const OPACITY_OPTIONS = [0, 25, 50, 75, 100] as const;
export const BG_OPACITY_OPTIONS: { value: number; label: string }[] = OPACITY_OPTIONS.map((v) => ({
  value: v,
  label: `${v}%`,
}));

const POS_PCTS = [0, 2, 5, 8, 10, 12, 15, 20, 25, 30] as const;
export const POS_PCT_OPTIONS: { value: number; label: string }[] = POS_PCTS.map((v) => ({
  value: v,
  label: `${v}%`,
}));

const TEXT_HEX: Record<string, string> = {
  white: "#ffffff",
  black: "#1a1a1a",
  yellow: "#fde047",
  cyan: "#67e8f9",
  green: "#86efac",
};

/** 背景纯色（不含透明度，透明度单独乘） */
const BG_RGB: Record<string, [number, number, number]> = {
  blue: [37, 99, 235],
  black: [15, 23, 42],
  white: [248, 250, 252],
  yellow: [202, 138, 4],
  transparent: [0, 0, 0],
};

const SIZE_BASE: Record<SubtitleTextSize, { x: number; y: number }> = {
  small: { x: 38, y: 22 },
  normal: { x: 49, y: 28 },
  large: { x: 60, y: 34 },
  xlarge: { x: 72, y: 42 },
};

export function defaultSubtitleAppearance(): SubtitleAppearance {
  return {
    text_size: "normal",
    text_color: "white",
    shadow: "shadow",
    bg_color: "blue",
    bg_opacity: 100,
    pos_bottom: 5,
    pos_top: 5,
  };
}

export function normalizeSubtitleAppearance(raw: Partial<SubtitleAppearance> | null | undefined): SubtitleAppearance {
  const d = defaultSubtitleAppearance();
  if (!raw || typeof raw !== "object") return d;
  const text_size =
    raw.text_size === "small" || raw.text_size === "large" || raw.text_size === "xlarge" ? raw.text_size : d.text_size;
  const text_color = typeof raw.text_color === "string" && raw.text_color in TEXT_HEX ? raw.text_color : d.text_color;
  const shadow =
    raw.shadow === "none" || raw.shadow === "strong" || raw.shadow === "shadow" ? raw.shadow : d.shadow;
  const bg_color =
    typeof raw.bg_color === "string" && raw.bg_color in BG_RGB ? raw.bg_color : d.bg_color;
  let bg_opacity = typeof raw.bg_opacity === "number" && Number.isFinite(raw.bg_opacity) ? Math.round(raw.bg_opacity) : d.bg_opacity;
  bg_opacity = Math.max(0, Math.min(100, bg_opacity));
  let pos_bottom = typeof raw.pos_bottom === "number" && Number.isFinite(raw.pos_bottom) ? Math.round(raw.pos_bottom) : d.pos_bottom;
  pos_bottom = Math.max(0, Math.min(30, pos_bottom));
  let pos_top = typeof raw.pos_top === "number" && Number.isFinite(raw.pos_top) ? Math.round(raw.pos_top) : d.pos_top;
  pos_top = Math.max(0, Math.min(30, pos_top));
  return {
    text_size,
    text_color,
    shadow,
    bg_color,
    bg_opacity,
    pos_bottom,
    pos_top,
  };
}

function textHex(key: string): string {
  return TEXT_HEX[key] || TEXT_HEX.white;
}

function bgRgba(key: string, opacityPct: number): string {
  if (key === "transparent") return "transparent";
  const [r, g, b] = BG_RGB[key] || BG_RGB.blue;
  const a = Math.max(0, Math.min(1, opacityPct / 100));
  return `rgba(${r},${g},${b},${a})`;
}

function shadowCss(shadow: SubtitleAppearance["shadow"]): string {
  switch (shadow) {
    case "none":
      return "none";
    case "strong":
      return "0 0 6px rgba(0,0,0,0.95), -1px -1px 0 rgba(0,0,0,0.9), 1px -1px 0 rgba(0,0,0,0.9), -1px 1px 0 rgba(0,0,0,0.9), 1px 1px 0 rgba(0,0,0,0.9)";
    default:
      return "-1px 1px 2px rgba(0,0,0,0.75), 1px 1px 2px rgba(0,0,0,0.75)";
  }
}

function subtitleFontSizePx(textSize: SubtitleTextSize): string {
  switch (textSize) {
    case "small":
      return "14px";
    case "large":
      return "20px";
    case "xlarge":
      return "24px";
    default:
      return "17px";
  }
}

/** PowerPlayer 6 subtitleStyle（合并进播放器默认样式） */
export function buildPowerPlayerSubtitleStyle(
  appearance: SubtitleAppearance | null | undefined,
  opts?: { autoSelect?: boolean }
): Record<string, string | boolean> {
  const a = normalizeSubtitleAppearance(appearance);
  let textShadow = "1px 1px #000";
  switch (a.shadow) {
    case "none":
      textShadow = "none";
      break;
    case "strong":
      textShadow = "0 0 6px #000, 1px 1px #000";
      break;
    default:
      break;
  }
  return {
    auto: opts?.autoSelect !== false,
    backgroundColor: bgRgba(a.bg_color, a.bg_color === "transparent" ? 0 : a.bg_opacity),
    fontWeight: "normal",
    fontSize: subtitleFontSizePx(a.text_size),
    color: textHex(a.text_color),
    textShadow,
  };
}

/** xgplayer TextTrack 的 style，会并入 xgplayer-subtitles 配置 */
export function buildXgTexttrackStyle(appearance: SubtitleAppearance | null | undefined): Record<string, unknown> {
  const a = normalizeSubtitleAppearance(appearance);
  const { x, y } = SIZE_BASE[a.text_size];
  return {
    fontColor: textHex(a.text_color),
    offsetBottom: a.pos_bottom,
    baseSizeX: x,
    baseSizeY: y,
    mode: "",
    fitVideo: true,
  };
}

/** 在播放器根节点上写 CSS 变量，由 index.css 中 .knox-subtitle-tuned 消费 */
export function applyKnoxSubtitleCssVars(root: HTMLElement | null, appearance: SubtitleAppearance | null | undefined) {
  if (!root) return;
  const a = normalizeSubtitleAppearance(appearance);
  root.classList.add("knox-subtitle-tuned");
  const bg = bgRgba(a.bg_color, a.bg_color === "transparent" ? 0 : a.bg_opacity);
  root.style.setProperty("--knox-sub-fg", textHex(a.text_color));
  root.style.setProperty("--knox-sub-bg", bg);
  root.style.setProperty("--knox-sub-shadow", shadowCss(a.shadow));
  root.style.setProperty("--knox-sub-font-size", subtitleFontSizePx(a.text_size));
  root.style.setProperty("--knox-sub-top-safe", `${a.pos_top}%`);
}

export function summarizeSubtitleAppearance(
  a: SubtitleAppearance | null | undefined,
  t: TranslateFn,
): string {
  const x = normalizeSubtitleAppearance(a);
  const textSizes = buildTextSizeOptions(t);
  const textColors = buildTextColorOptions(t);
  const bgColors = buildBgColorOptions(t);
  const shadows = buildShadowOptions(t);
  const size = optionLabel(textSizes, x.text_size, "settings.subtitle_appearance.options.text_size.normal", t);
  const textColor = optionLabel(textColors, x.text_color, "settings.subtitle_appearance.options.text_color.white", t);
  const bgColor = optionLabel(bgColors, x.bg_color, "settings.subtitle_appearance.options.bg_color.blue", t);
  const shadow = optionLabel(shadows, x.shadow, "settings.subtitle_appearance.options.shadow.shadow", t);
  return t("settings.subtitle_appearance.summary", {
    size,
    textColor,
    bgColor,
    opacity: x.bg_opacity,
    shadow,
    bottom: x.pos_bottom,
  });
}

export function previewSubtitleBoxStyle(a: SubtitleAppearance): CSSProperties {
  const x = normalizeSubtitleAppearance(a);
  return {
    display: "inline-block",
    maxWidth: "92%",
    padding: "6px 10px",
    borderRadius: 4,
    fontSize: x.text_size === "small" ? 13 : x.text_size === "large" ? 17 : x.text_size === "xlarge" ? 19 : 15,
    lineHeight: 1.35,
    color: textHex(x.text_color),
    backgroundColor: bgRgba(x.bg_color, x.bg_color === "transparent" ? 0 : x.bg_opacity),
    textShadow: shadowCss(x.shadow) as string,
    textAlign: "center",
  };
}
