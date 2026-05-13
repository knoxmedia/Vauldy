/** 外挂 VTT 字幕外观（xgplayer TextTrack / xgplayer-subtitles + Knox CSS 变量） */

import type { CSSProperties } from "react";
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

export const TEXT_SIZE_OPTIONS: { value: SubtitleTextSize; label: string }[] = [
  { value: "small", label: "小" },
  { value: "normal", label: "正常" },
  { value: "large", label: "大" },
  { value: "xlarge", label: "特大" },
];

export const TEXT_COLOR_OPTIONS: { value: string; label: string }[] = [
  { value: "white", label: "白色" },
  { value: "black", label: "黑色" },
  { value: "yellow", label: "黄色" },
  { value: "cyan", label: "青色" },
  { value: "green", label: "绿色" },
];

export const SHADOW_OPTIONS: { value: SubtitleAppearance["shadow"]; label: string }[] = [
  { value: "none", label: "无" },
  { value: "shadow", label: "投影" },
  { value: "strong", label: "重投影" },
];

export const BG_COLOR_OPTIONS: { value: string; label: string }[] = [
  { value: "blue", label: "蓝色" },
  { value: "black", label: "黑色" },
  { value: "white", label: "白色" },
  { value: "yellow", label: "黄色" },
  { value: "transparent", label: "透明" },
];

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
  const fs =
    a.text_size === "small"
      ? "14px"
      : a.text_size === "large"
        ? "20px"
        : a.text_size === "xlarge"
          ? "24px"
          : "17px";
  root.style.setProperty("--knox-sub-font-size", fs);
  root.style.setProperty("--knox-sub-top-safe", `${a.pos_top}%`);
}

export function summarizeSubtitleAppearance(a: SubtitleAppearance | null | undefined): string {
  const x = normalizeSubtitleAppearance(a);
  const ts = TEXT_SIZE_OPTIONS.find((o) => o.value === x.text_size)?.label || "正常";
  const tc = TEXT_COLOR_OPTIONS.find((o) => o.value === x.text_color)?.label || "白色";
  const bg = BG_COLOR_OPTIONS.find((o) => o.value === x.bg_color)?.label || "蓝色";
  const sh = SHADOW_OPTIONS.find((o) => o.value === x.shadow)?.label || "投影";
  return `大小 ${ts} · 字色 ${tc} · 背景 ${bg}（${x.bg_opacity}%）· ${sh} · 底距 ${x.pos_bottom}%`;
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
