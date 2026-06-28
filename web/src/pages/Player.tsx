import { Button, Input, Modal, Progress, Tag, message } from "antd";
import { ArrowLeftOutlined } from "@ant-design/icons";
import { useEffect, useId, useRef, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import Player, { TextTrack } from "xgplayer";
import HlsPlugin from "xgplayer-hls";
import ShakaPlugin from "xgplayer-shaka";
import "xgplayer/dist/index.min.css";
import "xgplayer/es/plugins/track/index.css";
import "xgplayer-subtitles/es/style/index.css";
import { useAuthStore } from "../store/auth";
import {
  fetchMediaSubtitles,
  reportPlaybackEnd,
  reportPlaybackStart,
  savePlaybackProgress,
} from "../api/client";
import {
  clearPlaylistPlaySession,
  isPlaylistPlaybackFinished,
  navigatePlaylistNext,
  buildPlaylistPageUrl,
} from "../lib/playlistPlayback";
import {
  clearAlbumPlaySession,
  isAlbumPlaybackFinished,
  navigateAlbumNext,
  buildAlbumPageUrl,
} from "../lib/albumPlayback";
import { isLikelyNaturalPlaybackEnd } from "../lib/playbackComplete";
import {
  clearSeriesPlaySession,
  isSeriesPlaybackFinished,
  navigateSeriesNext,
  buildSeriesPageUrl,
} from "../lib/seriesPlayback";
import { buildTextTrackListWithPrefs, buildPowerPlayerSubtitleList, normalizePlayerPrefs } from "../lib/playerPrefs";
import {
  applyKnoxSubtitleCssVars,
  buildPowerPlayerSubtitleStyle,
  buildXgTexttrackStyle,
} from "../lib/subtitleAppearance";
import { tGlobal, useT } from "../i18n";

/** fetch that aborts after timeoutMs (clears hung UI when backend blocks). */
async function fetchWithTimeout(
  input: string,
  init: RequestInit = {},
  timeoutMs: number
): Promise<Response> {
  const ctrl = new AbortController();
  const tid = window.setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    return await fetch(input, { ...init, signal: ctrl.signal });
  } finally {
    window.clearTimeout(tid);
  }
}

function withTimeout<T>(p: Promise<T>, ms: number, label: string): Promise<T> {
  return new Promise((resolve, reject) => {
    const t = window.setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms`)), ms);
    p.then(
      (v) => {
        window.clearTimeout(t);
        resolve(v);
      },
      (e) => {
        window.clearTimeout(t);
        reject(e);
      }
    );
  });
}

/** Subset of PowerPlayer 6 `.setup()` options supplied by GET /media/:id/hls (`powerplayer` in JSON). */
type PowerPlayerPlanFields = {
  base_url?: string;
  skin?: string;
  powerdrm_url?: string;
  weburlparam?: string;
  statistics_server?: string;
  client_cert?: string;
};

type PlaybackEngineId = "powerplayer" | "shaka" | "xgplayer";

/** Engine order from GET /media/:id/hls (`player_engine_order`), controlled by server `playback.engines`. */
type PlaybackPlan = {
  mode?: "native" | "hls" | "jit_hls" | "hls_drm" | "hls_aes_128" | "hls_powerdrm";
  playUrl?: string;
  /** MIME type of the source file (e.g. "video/mp4"), returned by /hls in native mode. */
  mime_type?: string;
  hls_master?: string;
  /** Present for Redis-free JIT; echoed on progress / playback logs for log-based session recovery. */
  session_id?: string;
  status?: string;
  task_id?: number;
  fallback?: string;
  /** Transport stream real-time encryption (library drm_enabled): play-time JIT, not batch package wait. */
  stream_drm?: boolean;
  player_engine_order?: string[];
  powerplayer?: PowerPlayerPlanFields;
  drm?: {
    widevine_license_url?: string;
    widevine_transport?: "json_local" | "raw";
    /** Optional; only when drm.widevine.emit_service_cert_url is true in server config. */
    widevine_service_cert_url?: string;
    fairplay_cert_url?: string;
    fairplay_license_url?: string;
    dash_mpd_url?: string;
    clearkey_keys?: Record<string, string>;
  };
};

function coalesceEngineOrder(plan: Pick<PlaybackPlan, "mode" | "player_engine_order">): string[] {
  const fromApi = plan.player_engine_order;
  if (Array.isArray(fromApi) && fromApi.length > 0) {
    return fromApi.map((s) => String(s).toLowerCase().trim()).filter(Boolean);
  }
  switch (plan.mode) {
    case "hls_powerdrm":
      return ["powerplayer"];
    case "hls_drm":
      return ["powerplayer", "shaka", "xgplayer"];
    default:
      return ["powerplayer", "xgplayer"];
  }
}

function defaultEngineOrderForMode(mode?: PlaybackPlan["mode"]): string[] {
  switch (mode) {
    case "hls_powerdrm":
      return ["powerplayer"];
    case "hls_drm":
      return ["powerplayer", "shaka", "xgplayer"];
    default:
      return ["powerplayer", "xgplayer"];
  }
}

function isPowerPlayerRuntimeAvailable(): boolean {
  if (typeof window === "undefined") return false;
  return typeof window.powerplayer === "function" || !!window.PowerPlayer;
}

function pickPlaybackEngine(
  order: string[],
  ctx: { hasWidevineFairplay: boolean; powerDRMOnly: boolean }
): PlaybackEngineId | null {
  for (const raw of order) {
    const e = String(raw).toLowerCase().trim();
    if (!e) continue;
    if (ctx.powerDRMOnly) {
      if (e === "powerplayer" && isPowerPlayerRuntimeAvailable()) return "powerplayer";
      continue;
    }
    if (ctx.hasWidevineFairplay) {
      if (e === "powerplayer" && isPowerPlayerRuntimeAvailable()) return "powerplayer";
      if (e === "shaka") return "shaka";
      if (e === "xgplayer") return "xgplayer";
      continue;
    }
    if (e === "shaka") continue;
    if (e === "powerplayer" && isPowerPlayerRuntimeAvailable()) return "powerplayer";
    if (e === "xgplayer") return "xgplayer";
  }
  return null;
}

type ShakaRequestLike = {
  uris?: string[];
  headers: Record<string, string>;
  method?: string;
  body?: BufferSource;
};

type PowerPlayerLike = {
  destroy?: () => void | Promise<void>;
  remove?: () => void | Promise<void>;
  on?: (event: string, cb: (...args: any[]) => void) => void;
  /** PowerPlayer 6+ lifecycle hooks (register callbacks). */
  onReady?: (cb: () => void) => void;
  onSeek?: (cb: (time: unknown) => void) => void;
  onTime?: (cb: (event: unknown) => void) => void;
  onComplete?: (cb: () => void) => void;
  onPause?: (cb: () => void) => void;
  onError?: (cb: (error: unknown) => void) => void;
};

/** Best-effort parse of duration from player time payloads. */
function readMediaDurationPayload(arg: unknown): number | null {
  if (typeof arg === "number" && Number.isFinite(arg) && arg > 0) return arg;
  if (arg && typeof arg === "object") {
    const o = arg as Record<string, unknown>;
    for (const k of ["duration", "totalTime", "length", "total"]) {
      const v = o[k];
      if (typeof v === "number" && Number.isFinite(v) && v > 0) return v;
    }
  }
  return null;
}

/** Best-effort parse of `onTime` / `onSeek` payload (SDK varies: number vs { position }). */
function readPowerPlayerTimePayload(arg: unknown): number | null {
  if (typeof arg === "number" && Number.isFinite(arg)) return arg;
  if (arg && typeof arg === "object") {
    const o = arg as Record<string, unknown>;
    for (const k of ["position", "time", "currentTime", "current", "seconds"]) {
      const v = o[k];
      if (typeof v === "number" && Number.isFinite(v)) return v;
    }
  }
  return null;
}

/** Return value of `powerplayer(containerId)` before `.setup()` (PowerPlayer 6 style). */
type PowerPlayerLegacyAPI = {
  setup: (config: Record<string, unknown>) => PowerPlayerLike;
};

declare global {
  interface Window {
    PowerPlayer?: new (options: Record<string, any>) => PowerPlayerLike;
    /** PowerPlayer 6: `powerplayer(containerId).setup({ ... })` */
    powerplayer?: (containerId: string) => PowerPlayerLegacyAPI;
  }
}

function toBase64(bytes: Uint8Array) {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 1) binary += String.fromCharCode(bytes[i]);
  return btoa(binary);
}

function fromBase64(b64: string) {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i += 1) out[i] = bin.charCodeAt(i);
  return out;
}

function toUint8Array(data: BufferSource | undefined) {
  if (!data) return new Uint8Array();
  if (data instanceof Uint8Array) return data;
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  return new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
}

export function adaptWidevineLicenseRequest(
  request: ShakaRequestLike,
  mediaId: number,
  transport: "json_local" | "raw" = "json_local"
) {
  const uri = String((request.uris && request.uris[0]) || "");
  if (!uri.includes("/drm/widevine/license")) return false;
  if (transport === "raw") {
    // Keep raw EME challenge bytes untouched, but include media_id so
    // backend can bind media -> KID and forward metadata headers upstream.
    try {
      const u = new URL(uri, window.location.origin);
      if (!u.searchParams.get("media_id")) u.searchParams.set("media_id", String(mediaId));
      request.uris = [u.toString()];
    } catch {
      const sep = uri.includes("?") ? "&" : "?";
      request.uris = [`${uri}${sep}media_id=${encodeURIComponent(String(mediaId))}`];
    }
    return true;
  }
  request.headers["Content-Type"] = "application/json";
  request.method = "POST";
  request.body = new TextEncoder().encode(
    JSON.stringify({ media_id: mediaId, challenge: toBase64(toUint8Array(request.body)) })
  );
  return true;
}

/** Shaka `util.Error.Category.DRM` === 6, `Code.INVALID_SERVER_CERTIFICATE` === 6004 */
export function isShakaInvalidWidevineServerCertificate(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const o = err as { category?: number; code?: number };
  return o.category === 6 && o.code === 6004;
}

/** Shaka `Category.PLAYER` === 7, `Code.LOAD_INTERRUPTED` === 7000 */
export function isShakaLoadInterrupted(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const o = err as { category?: number; code?: number };
  return o.category === 7 && o.code === 7000;
}

export function adaptWidevineLicenseResponse(
  data: BufferSource | undefined,
  transport: "json_local" | "raw" = "json_local"
) {
  const bytes = toUint8Array(data);
  if (transport === "raw") return bytes;
  const txt = new TextDecoder().decode(bytes);
  try {
    const parsed = JSON.parse(txt) as { license?: string; ckc?: string };
    const payload = parsed.license || parsed.ckc;
    if (payload) return fromBase64(payload);
  } catch {
    // keep raw response for non-JSON license servers
  }
  return bytes;
}

type TaskStatus = {
  task_id: number;
  status: "waiting" | "running" | "done" | "failed" | "cancelled";
  progress: number;
  ready: boolean;
  failed: boolean;
  hls_master?: string;
  poll_after_ms?: number;
};

type PreviewPlan = {
  enabled?: boolean;
  status?: "disabled" | "waiting" | "running" | "ready" | "failed";
  thumbnail?: {
    urls: string[];
    pic_num: number;
    width: number;
    height: number;
    col: number;
    row: number;
  };
};

type XgDefinition = {
  definition: string;
  text: string;
  url: string;
};

/** Maps backend `requireMediaAccess` / play handler error codes to user-readable text. */
function playbackForbiddenMessage(code: string): string {
  const c = (code || "").trim().toLowerCase();
  switch (c) {
    case "playback denied":
      return tGlobal("pages.player.denied_play_perm");
    case "library access denied":
      return tGlobal("pages.player.denied_library");
    case "folder access denied":
      return tGlobal("pages.player.denied_folder");
    case "outside parental allowed time":
      return tGlobal("pages.player.denied_time");
    case "parental pin required":
      return tGlobal("pages.player.denied_pin");
    default:
      if (!code) return tGlobal("pages.player.denied_default");
      return tGlobal("pages.player.denied_with_code", { code });
  }
}

class PlaybackPermissionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "PlaybackPermissionError";
  }
}

/** Client playback hints sent to `/hls` and echoed as `client_caps` for routing / logging. */
type ClientCaps = {
  videoCodecs: string[];
  audioCodecs: string[];
  maxHeight: number;
  qualities: string[];
  /** Container types the browser / Knox players can play (mp4/mkv/webm/ogg/flv). */
  containers: string[];
  /**
   * Compact MediaCapabilities decoding hints: `h264@1080:111` = supported/smooth/powerEfficient (0/1 each).
   * Populated for H.264/H.265 × each entry in `qualities` when `navigator.mediaCapabilities` is available.
   */
  mcap: string;
};

function qualityToDims(q: string): { width: number; height: number } {
  const h = parseInt(q, 10);
  if (h >= 1080) return { width: 1920, height: 1080 };
  if (h >= 720) return { width: 1280, height: 720 };
  if (h >= 480) return { width: 854, height: 480 };
  return { width: 640, height: 360 };
}

function h264CodecStringForHeight(h: number): string {
  if (h >= 1080) return "avc1.640028";
  if (h >= 720) return "avc1.4D401F";
  return "avc1.42E01E";
}

function bitrateForDims(h: number): number {
  if (h >= 1080) return 5_000_000;
  if (h >= 720) return 3_000_000;
  if (h >= 480) return 1_500_000;
  return 800_000;
}

/** Probes smooth / powerEfficient via MediaCapabilities for H.264 & H.265 at each ladder height. */
async function collectDecodingMcaps(qualities: string[]): Promise<string> {
  const mc = typeof navigator !== "undefined" ? navigator.mediaCapabilities : undefined;
  if (!mc?.decodingInfo) return "";

  const jobs: Promise<string>[] = [];
  for (const q of qualities) {
    const { width, height } = qualityToDims(q);
    const br = bitrateForDims(height);
    const h264Codec = h264CodecStringForHeight(height);
    const h265Mime = 'video/mp4; codecs="hvc1.1.6.L93.B0"';
    const h264Mime = `video/mp4; codecs="${h264Codec}"`;
    const baseVideo = { width, height, bitrate: br, framerate: 30 as const };

    jobs.push(
      mc
        .decodingInfo({
          type: "media-source",
          video: { contentType: h264Mime, ...baseVideo },
        })
        .then(
          (r) =>
            `h264@${height}:${r.supported ? 1 : 0}${r.smooth ? 1 : 0}${r.powerEfficient ? 1 : 0}`
        )
        .catch(() => `h264@${height}:000`)
    );
    jobs.push(
      mc
        .decodingInfo({
          type: "media-source",
          video: { contentType: h265Mime, ...baseVideo },
        })
        .then(
          (r) =>
            `h265@${height}:${r.supported ? 1 : 0}${r.smooth ? 1 : 0}${r.powerEfficient ? 1 : 0}`
        )
        .catch(() => `h265@${height}:000`)
    );
  }

  const parts = await Promise.all(jobs);
  return parts.join(",");
}

async function detectClientCaps(): Promise<ClientCaps> {
  const probe = document.createElement("video");
  const supports = (mime: string) => {
    try {
      return probe.canPlayType(mime) !== "";
    } catch {
      return false;
    }
  };

  const containers: string[] = [];
  if (supports("video/mp4") || supports('video/mp4; codecs="avc1.42E01E"')) containers.push("mp4");
  if (supports("video/x-matroska") || supports('video/x-matroska; codecs="avc1.42E01E"')) containers.push("mkv");
  if (supports("video/webm") || supports('video/webm; codecs="vp09.00.10.08"')) containers.push("webm");
  if (supports("video/ogg") || supports("application/ogg") || supports('video/ogg; codecs="theora"'))
    containers.push("ogg");

  const videoCodecs: string[] = [];
  if (supports('video/mp4; codecs="avc1.42E01E"')) videoCodecs.push("h264");
  if (supports('video/mp4; codecs="hvc1.1.6.L93.B0"') || supports('video/mp4; codecs="hev1.1.6.L93.B0"')) videoCodecs.push("h265");
  if (supports('video/mp4; codecs="av01.0.05M.08"') || supports('video/webm; codecs="av1"')) videoCodecs.push("av1");
  if (
    supports('video/webm; codecs="vp09.00.10.08"') ||
    supports('video/webm; codecs="vp9"') ||
    supports("video/webm; codecs=vp9")
  ) {
    videoCodecs.push("vp9");
  }
  // FLV is not exposed via canPlayType in Chromium, but PowerPlayer / xgplayer demux FLV in JS
  // when the inner video/audio codecs are supported.
  if (videoCodecs.includes("h264") || videoCodecs.includes("h265")) {
    containers.push("flv");
  }

  const audioCodecs: string[] = [];
  if (supports('audio/mp4; codecs="mp4a.40.2"')) audioCodecs.push("aac");
  if (supports("audio/mpeg")) audioCodecs.push("mp3");
  if (supports('audio/webm; codecs="opus"')) audioCodecs.push("opus");
  if (
    supports('audio/mp4; codecs="ac-3"') ||
    supports("audio/ac3") ||
    supports('video/mp4; codecs="avc1.42E01E, ac-3"')
  ) {
    audioCodecs.push("ac3");
  }
  if (supports('audio/mp4; codecs="ec-3"') || supports('video/mp4; codecs="avc1.42E01E, ec-3"')) {
    audioCodecs.push("eac3");
  }

  const maxHeight = Math.max(360, Math.min(1080, window.screen?.height || 1080));
  const qualities = ["360p", "480p", "720p", "1080p"].filter((q) => parseInt(q, 10) <= maxHeight);
  const mcap = await collectDecodingMcaps(qualities);

  return { videoCodecs, audioCodecs, maxHeight, qualities, containers, mcap };
}

export default function PlayerPage() {
  const t = useT();
  const { id } = useParams();
  const [searchParams] = useSearchParams();
  const searchParamsRef = useRef(searchParams);
  searchParamsRef.current = searchParams;
  const nav = useNavigate();
  const domId = useId().replace(/:/g, "");
  /** Stable mount node for players; avoids Strict Mode race where getElementById fails mid-async. */
  const playerMountRef = useRef<HTMLDivElement | null>(null);
  const playerRef = useRef<Player | null>(null);
  const drmPlayerRef = useRef<any>(null);
  const drmVideoRef = useRef<HTMLVideoElement | null>(null);
  const powerPlayerRef = useRef<PowerPlayerLike | null>(null);
  /** Last playback plan `powerplayer` block (for PowerPlayer setup when not passed explicitly). */
  const powerPlayerPlanRef = useRef<PowerPlayerPlanFields | undefined>(undefined);
  /** Last plan engine order + mode (e.g. poll transcode completion → HLS with same priority). */
  const playbackPlanMetaRef = useRef<{
    engineOrder: string[];
    planMode: PlaybackPlan["mode"];
  }>({ engineOrder: ["powerplayer", "xgplayer"], planMode: "hls" });
  const [mid, setMid] = useState<number | undefined>(
    id ? Number(id) : Number(searchParams.get("id") || "")
  );
  const token = useAuthStore((s) => s.token);
  const canPlay = useAuthStore((s) => s.canPlay);
  const [showBack, setShowBack] = useState(true);
  const [loadingText, setLoadingText] = useState(() => tGlobal("pages.player.preparing"));
  const [parentalUnlockToken, setParentalUnlockToken] = useState<string>("");
  const [transcodeProgress, setTranscodeProgress] = useState<number>(0);
  const [transcodeStatus, setTranscodeStatus] = useState<"waiting" | "running" | null>(null);
  const [playlistFinished, setPlaylistFinished] = useState(false);
  const [seriesFinished, setSeriesFinished] = useState(false);
  const playbackStartedRef = useRef(false);
  const playbackEndedRef = useRef(false);
  const lastProgressSecRef = useRef(0);
  const lastProgressAtRef = useRef(0);
  const lastProgressSaveAtRef = useRef(0);
  const mediaDurationSecRef = useRef(0);
  const sourceFallbackTriedRef = useRef(false);
  const noAudioRetryTriedRef = useRef(false);
  const noAudioRetryInFlightRef = useRef(false);
  const hideTimerRef = useRef<number | null>(null);
  /** Bumped on effect cleanup so stale async work (e.g. React Strict Mode) does not abort the active Shaka session (7000). */
  const playbackGenerationRef = useRef(0);
  /** JIT session id from HLS plan (`session_id`); attached to playback/progress API calls for access-log correlation. */
  const jitPlaybackSessionIdRef = useRef<string | null>(null);
  /** Incremented while Shaka DRM is initializing/recursing so teardown of <video> does not trigger prefer_source xgplayer fallback. */
  const drmRecoveryDepthRef = useRef(0);
  const startSec = (() => {
    const t = searchParams.get("t");
    if (!t) return 0;
    const n = parseInt(t, 10);
    return Number.isFinite(n) && n >= 0 ? n : 0;
  })();

  useEffect(() => {
    if (id) {
      const n = Number(id);
      if (!Number.isNaN(n)) setMid(n);
    }
  }, [id]);

  useEffect(() => {
    setPlaylistFinished(false);
    setSeriesFinished(false);
  }, [mid]);

  useEffect(() => {
    if (!mid || Number.isNaN(mid)) {
      setLoadingText(t("pages.player.no_media_id"));
      return;
    }
    if (!token) {
      setLoadingText(t("pages.player.waiting_login"));
      return;
    }
    if (canPlay === false) {
      setTranscodeStatus(null);
      setLoadingText(t("pages.player.no_play_perm_title"));
      message.warning(t("pages.player.no_play_perm_detail"));
      return;
    }
    const sessionGen = ++playbackGenerationRef.current;
    const isStale = () => playbackGenerationRef.current !== sessionGen;
    playbackStartedRef.current = false;
    playbackEndedRef.current = false;
    lastProgressSecRef.current = 0;
    lastProgressAtRef.current = 0;
    lastProgressSaveAtRef.current = 0;
    mediaDurationSecRef.current = 0;
    jitPlaybackSessionIdRef.current = null;
    const hostEl = playerMountRef.current ?? document.getElementById(domId);
    if (hostEl) hostEl.innerHTML = "";
    const capsPromise = detectClientCaps();
    let timer: number | null = null;
    const withPlaybackLog = <T extends Record<string, unknown>>(p: T): T & { session_id?: string } => {
      const sid = jitPlaybackSessionIdRef.current?.trim();
      if (!sid) return p;
      return { ...p, session_id: sid };
    };
    const dbg = (...args: any[]) => console.log("[player]", ...args);
    const dbgErr = (...args: any[]) => console.error("[player]", ...args);
    const fetchPreviewPlan = async (): Promise<PreviewPlan | null> => {
      try {
        const resp = await fetchWithTimeout(
          `/api/v1/media/${mid}/preview?access_token=${encodeURIComponent(token)}`,
          {},
          15_000
        );
        if (!resp.ok) return null;
        return (await resp.json()) as PreviewPlan;
      } catch {
        return null;
      }
    };
    const fetchHlsDefinitions = async (masterURL: string): Promise<XgDefinition[]> => {
      try {
        const resp = await fetchWithTimeout(masterURL, {}, 45_000);
        if (!resp.ok) return [];
        const txt = await resp.text();
        const lines = txt.split(/\r?\n/);
        const defs: XgDefinition[] = [];
        for (let i = 0; i < lines.length; i += 1) {
          const ln = (lines[i] || "").trim();
          if (!ln.startsWith("#EXT-X-STREAM-INF:")) continue;
          const next = (lines[i + 1] || "").trim();
          if (!next || next.startsWith("#")) continue;
          let abs = new URL(next, masterURL).toString();
          if (token && !abs.includes("access_token=")) {
            abs = `${abs}${abs.includes("?") ? "&" : "?"}access_token=${encodeURIComponent(token)}`;
          }
          if (parentalUnlockToken && !abs.includes("parental_unlock=")) {
            abs = `${abs}${abs.includes("?") ? "&" : "?"}parental_unlock=${encodeURIComponent(parentalUnlockToken)}`;
          }
          const resMatch = ln.match(/RESOLUTION=(\d+)x(\d+)/i);
          const h = resMatch ? Number(resMatch[2]) : 0;
          const d = h > 0 ? `${h}p` : `L${defs.length + 1}`;
          defs.push({ definition: d, text: d, url: abs });
        }
        return defs;
      } catch {
        return [];
      }
    };
    const safeSeconds = (v: number) => {
      if (!Number.isFinite(v) || v < 0) return null;
      return Math.floor(v);
    };

    const readLivePlaybackSnapshot = () => {
      let position = lastProgressSecRef.current;
      let duration = mediaDurationSecRef.current;
      let mediaEnded = false;
      const started = playbackStartedRef.current;

      if (!started) {
        return { position, duration, started, mediaEnded };
      }

      const xg = playerRef.current as { currentTime?: number; duration?: number; ended?: boolean } | null;
      if (xg) {
        const cur = safeSeconds(Number(xg.currentTime || 0));
        const dur = safeSeconds(Number(xg.duration || 0));
        if (cur !== null) position = Math.max(position, cur);
        if (dur !== null && dur > 0) duration = dur;
        if (xg.ended) mediaEnded = true;
      }
      const video = drmVideoRef.current;
      if (video) {
        const cur = safeSeconds(video.currentTime);
        const dur = safeSeconds(video.duration);
        if (cur !== null) position = Math.max(position, cur);
        if (dur !== null && dur > 0) duration = dur;
        if (video.ended) mediaEnded = true;
      }
      const pp = powerPlayerRef.current as Record<string, unknown> | null;
      if (pp) {
        for (const method of ["getPosition", "getCurrentTime", "getPlayTime", "getTime"]) {
          const fn = pp[method];
          if (typeof fn !== "function") continue;
          try {
            const raw = (fn as () => unknown).call(pp);
            const parsed = readPowerPlayerTimePayload(raw);
            const sec = parsed !== null ? safeSeconds(parsed) : safeSeconds(Number(raw));
            if (sec !== null && sec > 0) {
              position = Math.max(position, sec);
              break;
            }
          } catch {
            // ignore unsupported getter
          }
        }
        for (const method of ["getDuration", "getTotalTime", "getLength"]) {
          const fn = pp[method];
          if (typeof fn !== "function") continue;
          try {
            const raw = (fn as () => unknown).call(pp);
            const parsed = readMediaDurationPayload(raw);
            const sec = parsed !== null ? safeSeconds(parsed) : safeSeconds(Number(raw));
            if (sec !== null && sec > 0) {
              duration = sec;
              break;
            }
          } catch {
            // ignore unsupported getter
          }
        }
      }
      const host = playerMountRef.current ?? document.getElementById(domId);
      const htmlVideo = host?.querySelector("video");
      if (htmlVideo) {
        const cur = safeSeconds(htmlVideo.currentTime);
        const dur = safeSeconds(htmlVideo.duration);
        if (cur !== null) position = Math.max(position, cur);
        if (dur !== null && dur > 0) duration = dur;
        if (htmlVideo.ended) mediaEnded = true;
      }
      return { position, duration, started, mediaEnded };
    };

    const handleMediaPlaybackComplete = () => {
      if (isStale() || !mid || playbackEndedRef.current) return;
      const live = readLivePlaybackSnapshot();
      if (
        !isLikelyNaturalPlaybackEnd(
          live.started,
          live.position,
          live.duration,
          live.mediaEnded
        )
      ) {
        dbg("ignore premature playback complete", live);
        return;
      }
      playbackEndedRef.current = true;
      lastProgressSecRef.current = live.position;
      if (live.duration > 0) mediaDurationSecRef.current = live.duration;
      const endPos = live.position;
      void savePlaybackProgress(mid, withPlaybackLog({ position: endPos, completed: 1 })).catch(() => {});
      void reportPlaybackEnd(mid, withPlaybackLog({ position: endPos, completed: 1 })).catch(() => {});
      const params = searchParamsRef.current;
      const advanced =
        navigatePlaylistNext(nav, params, mid) ||
        navigateSeriesNext(nav, params, mid) ||
        navigateAlbumNext(nav, params, mid);
      if (!advanced && isPlaylistPlaybackFinished(params, mid)) {
        clearPlaylistPlaySession();
        setPlaylistFinished(true);
        setShowBack(true);
        message.info(t("pages.player.playlist_done"));
      } else if (!advanced && isSeriesPlaybackFinished(params, mid)) {
        clearSeriesPlaySession();
        setSeriesFinished(true);
        setShowBack(true);
        message.info(t("pages.player.series_done"));
      } else if (!advanced && isAlbumPlaybackFinished(params, mid)) {
        clearAlbumPlaySession();
        setShowBack(true);
        message.info(t("pages.player.album_done"));
      }
    };

    const attachPowerPlayerEvents = (pp: PowerPlayerLike) => {
      const bind = (method: keyof PowerPlayerLike, fn: (...args: any[]) => void) => {
        const m = pp[method];
        if (typeof m !== "function") return;
        try {
          (m as (...a: any[]) => void).call(pp, fn);
        } catch (e) {
          dbgErr(`powerplayer ${String(method)} bind failed`, e);
        }
      };

      bind("onReady", () => {
        if (isStale()) return;
        dbg("powerplayer onReady");
        setLoadingText("");
      });

      bind("onSeek", (time: unknown) => {
        if (isStale() || !mid) return;
        const raw = readPowerPlayerTimePayload(time);
        const sec = raw !== null ? safeSeconds(raw) : null;
        dbg("powerplayer onSeek", time);
        if (sec !== null) void savePlaybackProgress(mid, withPlaybackLog({ position: sec, completed: 0 })).catch(() => {});
      });

      bind("onTime", (event: unknown) => {
        if (isStale() || !mid) return;
        const dur = readMediaDurationPayload(event);
        if (dur !== null) mediaDurationSecRef.current = Math.floor(dur);
        const pos = readPowerPlayerTimePayload(event);
        if (pos === null) return;
        const cur = safeSeconds(pos);
        if (cur === null || cur <= 0) return;
        if (!playbackStartedRef.current) {
          playbackStartedRef.current = true;
          void reportPlaybackStart(mid, withPlaybackLog({ position: cur, completed: 0 })).catch(() => {});
        }
        lastProgressSecRef.current = cur;
        lastProgressAtRef.current = Date.now();
        const now = Date.now();
        if (now - lastProgressSaveAtRef.current < 9000) return;
        lastProgressSaveAtRef.current = now;
        void savePlaybackProgress(mid, withPlaybackLog({ position: cur, completed: 0 })).catch(() => {});
      });

      const onPowerPlayerComplete = (event?: unknown) => {
        if (isStale()) return;
        const pos = readPowerPlayerTimePayload(event);
        if (pos !== null) {
          const cur = safeSeconds(pos);
          if (cur !== null && cur > 0) lastProgressSecRef.current = cur;
        }
        const dur = readMediaDurationPayload(event);
        if (dur !== null) mediaDurationSecRef.current = Math.floor(dur);
        dbg("powerplayer onComplete");
        handleMediaPlaybackComplete();
      };
      bind("onComplete", onPowerPlayerComplete);

      bind("onPause", () => {
        if (isStale() || !mid) return;
        dbg("powerplayer onPause");
        const cur = lastProgressSecRef.current;
        if (cur > 0) void savePlaybackProgress(mid, withPlaybackLog({ position: cur, completed: 0 })).catch(() => {});
      });

      bind("onError", (error: unknown) => {
        dbgErr("powerplayer onError", error);
      });
    };
    const destroyDRMPlayer = async () => {
      if (drmPlayerRef.current) {
        await drmPlayerRef.current.destroy();
        drmPlayerRef.current = null;
      }
      if (drmVideoRef.current) {
        drmVideoRef.current.pause();
        drmVideoRef.current.src = "";
        drmVideoRef.current.remove();
        drmVideoRef.current = null;
      }
    };
    const destroyPowerPlayer = async () => {
      if (!powerPlayerRef.current) return;
      const pp = powerPlayerRef.current;
      try {
        await pp.destroy?.();
      } catch {
        // ignore
      }
      try {
        await pp.remove?.();
      } catch {
        // ignore
      } finally {
        powerPlayerRef.current = null;
      }
    };
    const resolvePlayerHost = (): HTMLElement | null =>
      playerMountRef.current ?? document.getElementById(domId);

    const playWithShakaDRM = async (
      manifestURL: string,
      drm: NonNullable<PlaybackPlan["drm"]>,
      opts?: { omitWidevineServiceCert?: boolean; loadInterruptRetry?: boolean }
    ) => {
      const omitWidevineServiceCert = !!opts?.omitWidevineServiceCert;
      dbg("playWithShakaDRM start", { mid, manifestURL, drm, omitWidevineServiceCert });
      drmRecoveryDepthRef.current++;
      try {
      const shaka = await import("shaka-player/dist/shaka-player.ui.js");
      if (isStale()) return;
      const shakaPlayer = (shaka as any).default || shaka;
      shakaPlayer.polyfill.installAll();
      const host = resolvePlayerHost();
      if (!host) {
        if (isStale()) return;
        throw new Error("player mount missing");
      }
      host.innerHTML = "";
      const video = document.createElement("video");
      video.style.width = "100%";
      video.style.height = "100%";
      video.autoplay = true;
      video.playsInline = true;
      video.setAttribute("playsinline", "");
      video.setAttribute("webkit-playsinline", "");
      video.controls = true;
      host.appendChild(video);
      drmVideoRef.current = video;
      const player = new shakaPlayer.Player();
      await player.attach(video);
      if (isStale()) {
        await player.destroy().catch(() => {});
        drmPlayerRef.current = null;
        return;
      }
      drmPlayerRef.current = player;
      player.addEventListener("error", (evt: any) => {
        const d = evt?.detail || evt;
        dbgErr("shaka error event", d, "data=", d?.data);
        const dataMsg = Array.isArray(d?.data) ? String(d.data[2] || "") : "";
        if (!noAudioRetryTriedRef.current && dataMsg.includes("AUDIO_RENDERER_ERROR")) {
          noAudioRetryTriedRef.current = true;
          noAudioRetryInFlightRef.current = true;
          const noAudioURL = appendToken(appendQueryValue(manifestURL, "no_audio", "1"));
          dbg("retry shaka with no_audio=1", { noAudioURL });
          void (async () => {
            try {
              await destroyDRMPlayer();
              if (isStale()) return;
              await playWithShakaDRM(noAudioURL, drm, opts);
            } finally {
              noAudioRetryInFlightRef.current = false;
            }
          })().catch((e) => dbgErr("no-audio retry failed", e));
          return;
        }
      });
      // ClearKey is debug-only. Production/default path should use Widevine
      // license requests so we only enable ClearKey when explicitly requested.
      const clearKeyDebugEnabled =
        new URLSearchParams(window.location.search).get("clearkey") === "1";
      const hasClearKeys =
        clearKeyDebugEnabled &&
        !!(drm.clearkey_keys && Object.keys(drm.clearkey_keys).length > 0);
      const widevineTransport = drm.widevine_transport || "json_local";
      const drmAdvanced: Record<string, { serverCertificateUri: string }> = {};
      if (drm.fairplay_cert_url) {
        drmAdvanced["com.apple.fps"] = { serverCertificateUri: drm.fairplay_cert_url };
      }
      if (drm.widevine_service_cert_url && !omitWidevineServiceCert) {
        drmAdvanced["com.widevine.alpha"] = { serverCertificateUri: drm.widevine_service_cert_url };
      }
      player.configure({
        drm: {
          ...(hasClearKeys ? { clearKeys: drm.clearkey_keys } : {}),
          ...(hasClearKeys
            ? {
                // HLS currently signals Widevine UUID in EXT-X-KEY/SESSION-KEY.
                // Force Shaka to satisfy it with ClearKey during local debugging.
                keySystemsMapping: {
                  "com.widevine.alpha": "org.w3.clearkey",
                },
              }
            : {}),
          servers: hasClearKeys
            ? {}
            : {
                ...(drm.widevine_license_url ? { "com.widevine.alpha": drm.widevine_license_url } : {}),
                ...(drm.fairplay_license_url ? { "com.apple.fps": drm.fairplay_license_url } : {}),
              },
          ...(Object.keys(drmAdvanced).length > 0 ? { advanced: drmAdvanced } : {}),
        },
      });
      const engine = player.getNetworkingEngine();
      if (engine) {
        engine.registerRequestFilter((type: number, request: any) => {
          if (token) {
            request.headers["Authorization"] = `Bearer ${token}`;
          }
          if (hasClearKeys) return;
          if (type !== shakaPlayer.net.NetworkingEngine.RequestType.LICENSE || !mid) return;
          dbg("license request", {
            uri: String((request.uris && request.uris[0]) || ""),
            method: request.method,
          });
          if (adaptWidevineLicenseRequest(request, mid, widevineTransport)) return;
          const uri = String((request.uris && request.uris[0]) || "");
          if (uri.includes("/drm/fairplay/license")) {
            request.headers["Content-Type"] = "application/json";
            request.method = "POST";
            request.body = new TextEncoder().encode(
              JSON.stringify({ media_id: mid, spc: toBase64(toUint8Array(request.body)) })
            );
          }
        });
        if (!hasClearKeys) {
          engine.registerResponseFilter((type: number, response: any) => {
            if (type !== shakaPlayer.net.NetworkingEngine.RequestType.LICENSE) return;
            dbg("license response", { bytes: response?.data?.byteLength || 0 });
            response.data = adaptWidevineLicenseResponse(response.data, widevineTransport);
            dbg("license response adapted", { widevineTransport });
          });
        }
      }
      video.addEventListener("error", () => {
        if (drmRecoveryDepthRef.current > 0) {
          dbg("skip source fallback during Shaka DRM setup/recovery");
          return;
        }
        if (drmPlayerRef.current) {
          dbg("skip HTMLMediaElement error fallback while Shaka instance attached");
          return;
        }
        if (noAudioRetryInFlightRef.current) {
          dbg("skip source fallback while no-audio retry in-flight");
          return;
        }
        if (sourceFallbackTriedRef.current || !mid) return;
        sourceFallbackTriedRef.current = true;
        setTranscodeStatus(null);
        setLoadingText(t("pages.player.drm_fallback"));
        const sourceURL = appendToken(`/api/v1/media/${mid}/play?prefer_source=1`);
        void fetchPreviewPlan().then(async (previewPlan) => {
          await playWithURL(sourceURL, previewPlan);
        });
      });
      video.addEventListener("play", () => {
        if (playbackStartedRef.current || !mid) return;
        playbackStartedRef.current = true;
        const cur = safeSeconds(video.currentTime);
        if (cur === null) return;
        void reportPlaybackStart(mid, withPlaybackLog({ position: cur, completed: 0 })).catch(() => {});
      });
      video.addEventListener("timeupdate", () => {
        if (!mid) return;
        const dur = safeSeconds(video.duration);
        if (dur !== null && dur > 0) mediaDurationSecRef.current = dur;
        const cur = safeSeconds(video.currentTime);
        if (cur === null || cur <= 0) return;
        lastProgressSecRef.current = cur;
        lastProgressAtRef.current = Date.now();
        const now = Date.now();
        if (now - lastProgressSaveAtRef.current < 9000) return;
        lastProgressSaveAtRef.current = now;
        void savePlaybackProgress(mid, withPlaybackLog({ position: cur, completed: 0 })).catch(() => {});
      });
      video.addEventListener("ended", () => {
        if (isStale() || playbackEndedRef.current || !mid) return;
        handleMediaPlaybackComplete();
      });
      if (isStale()) {
        await player.destroy().catch(() => {});
        drmPlayerRef.current = null;
        return;
      }
      try {
        await player.load(manifestURL);
      } catch (err) {
        if (
          !omitWidevineServiceCert &&
          drm.widevine_service_cert_url &&
          isShakaInvalidWidevineServerCertificate(err)
        ) {
          dbg(
            "Widevine server certificate rejected by CDM (6004); retrying without serverCertificateUri",
            err
          );
          await destroyDRMPlayer();
          if (isStale()) return;
          await new Promise((r) => window.setTimeout(r, 48));
          if (isStale()) return;
          await playWithShakaDRM(manifestURL, drm, { omitWidevineServiceCert: true });
          return;
        }
        if (
          omitWidevineServiceCert &&
          !opts?.loadInterruptRetry &&
          isShakaLoadInterrupted(err) &&
          !isStale()
        ) {
          dbg("Shaka load interrupted (7000) after dropping service cert; retrying once", err);
          await destroyDRMPlayer();
          await new Promise((r) => window.setTimeout(r, 64));
          if (isStale()) return;
          await playWithShakaDRM(manifestURL, drm, {
            omitWidevineServiceCert: true,
            loadInterruptRetry: true,
          });
          return;
        }
        throw err;
      }
      if (isStale()) {
        await player.destroy().catch(() => {});
        drmPlayerRef.current = null;
        return;
      }
      dbg("shaka load success", { manifestURL });
      setLoadingText("");
      } finally {
        drmRecoveryDepthRef.current--;
      }
    };
    type PlayWithURLOptions = {
      drm?: PlaybackPlan["drm"];
      engineOrder?: string[];
      planMode?: PlaybackPlan["mode"];
      powerPlayerCfg?: PowerPlayerPlanFields | null;
      mimeType?: string;
    };

    const playWithURL = async (url: string, preview?: PreviewPlan | null, opts?: PlayWithURLOptions) => {
      if (isStale()) return;
      playerRef.current?.destroy();
      await destroyDRMPlayer();
      await destroyPowerPlayer();
      playbackStartedRef.current = false;
      playbackEndedRef.current = false;
      lastProgressSaveAtRef.current = 0;
      mediaDurationSecRef.current = 0;

      const drm = opts?.drm;
      const planMode = opts?.planMode;
      const powerPlayerCfg = opts?.powerPlayerCfg;
      const mimeType = opts?.mimeType;
      const engineOrder =
        opts?.engineOrder && opts.engineOrder.length > 0
          ? opts.engineOrder
          : defaultEngineOrderForMode(planMode);

      const hasWidevineFairplay = !!(drm?.widevine_license_url || drm?.fairplay_license_url);
      const powerDRMOnly = planMode === "hls_powerdrm";

      let chosen = pickPlaybackEngine(engineOrder, { hasWidevineFairplay, powerDRMOnly });
      if (chosen === null) {
        if (powerDRMOnly) {
          throw new Error(t("pages.player.no_powerplayer"));
        }
        chosen = hasWidevineFairplay ? "shaka" : "xgplayer";
      }

      dbg("playback engine choice", {
        chosen,
        engineOrder,
        planMode,
        hasWidevineFairplay,
        powerDRMOnly,
      });

      const playerPrefs = normalizePlayerPrefs(useAuthStore.getState().playerPrefs);
      let textTrackList: ReturnType<typeof buildTextTrackListWithPrefs>["list"] = [];
      let textTrackDefaultOpen = false;
      if (mid) {
        try {
          const rows = await withTimeout(fetchMediaSubtitles(mid), 12_000, "subtitles");
          const built = buildTextTrackListWithPrefs(mid, token, rows, playerPrefs);
          textTrackList = built.list;
          textTrackDefaultOpen = built.isDefaultOpen;
        } catch {
          textTrackList = [];
          textTrackDefaultOpen = false;
        }
      }
      const powerPlayerSubtitles = buildPowerPlayerSubtitleList(textTrackList);
      const powerPlayerSubtitleStyle = buildPowerPlayerSubtitleStyle(playerPrefs.subtitle_appearance, {
        autoSelect: playerPrefs.auto_select && playerPrefs.subtitle_mode !== "off",
      });
      const powerPlayerTranslateApi = `/api/v1/subtitles/translate?access_token=${encodeURIComponent(token)}`;

      if (chosen === "powerplayer") {
        const host = resolvePlayerHost();
        if (!host) throw new Error("player mount missing");
        host.innerHTML = "";
        const legacyFn = window.powerplayer;
        if (typeof legacyFn === "function") {
          const isHls = /\.m3u8(\?|#|$)/i.test(url);
          const ppSetup = powerPlayerCfg ?? powerPlayerPlanRef.current;
          const baseUrl = ppSetup?.base_url?.trim() || "/static/powerplayer6";
          const skin = ppSetup?.skin?.trim() || "skin.zip";
          const clientcert = ppSetup?.client_cert?.trim() || "powerplayer";
          const statisticsserver = ppSetup?.statistics_server?.trim() || "";
          const weburlparam = ppSetup?.weburlparam?.trim() || "";
          const powerdrmurl = ppSetup?.powerdrm_url?.trim() || "";
          const pp = legacyFn(domId).setup({
            modes: [{ type: "html5" }],
            baseUrl,
            skin,
            fileid: "",
            contentid: "",
            siteid: "",
            file: url,
            height: "100%",
            width: "100%",
            streamid: "",
            code: "",
            username: "",
            headtime: "0",
            bottomtime: "0",
            starttime: "",
            endtime: "",
            title: "",
            rid: "",
            statisticsserver,
            weburlparam,
            backcolor: "161616",
            showrighttoolbar: true,
            pip: true,
            autostart: true,
            playsinline: true,
            provider: isHls ? "hls" : "http",
            latencythreshold: 1,
            "http.startparam": "start",
            "shortcuts.step": 10,
            seamless: true,
            lastplayposition: startSec > 0 ? startSec : 0,
            seekdisabled: false,
            fullscreendisabled: false,
            bulletscreen: false,
            showthumbnails: false,
            screenshot: true,
            clientcert,
            powerdrmurl,
            ...(mimeType ? { mimeType } : {}),
            ...(powerPlayerSubtitles.length > 0
              ? {
                  subtitle: powerPlayerSubtitles,
                  subtitleStyle: powerPlayerSubtitleStyle,
                  translateApi: powerPlayerTranslateApi,
                }
              : {}),
          });
          powerPlayerRef.current = pp;
          attachPowerPlayerEvents(pp);
          dbg("powerplayer legacy setup", { url, provider: isHls ? "hls" : "http" });
          setLoadingText("");
          return;
        }
        const PowerPlayer = window.PowerPlayer;
        if (!PowerPlayer) {
          throw new Error("PowerPlayer is not available (need window.powerplayer or window.PowerPlayer)");
        }
        const pp = new PowerPlayer({
          id: domId,
          url,
          autoplay: true,
          playsinline: true,
          width: "100%",
          height: "100%",
          ...(powerPlayerSubtitles.length > 0
            ? {
                subtitle: powerPlayerSubtitles,
                subtitleStyle: powerPlayerSubtitleStyle,
                translateApi: powerPlayerTranslateApi,
              }
            : {}),
        });
        powerPlayerRef.current = pp;
        attachPowerPlayerEvents(pp);
        dbg("powerplayer constructor init", { url });
        setLoadingText("");
        return;
      }

      if (chosen === "shaka" && hasWidevineFairplay && drm) {
        const manifestURL = drm.dash_mpd_url ? appendToken(drm.dash_mpd_url) : url;
        dbg("switch to standalone Shaka DRM", { url: manifestURL, drm });
        await playWithShakaDRM(manifestURL, drm);
        return;
      }

      if (chosen === "xgplayer" && hasWidevineFairplay && drm) {
        const drmURL = drm.dash_mpd_url ? appendToken(drm.dash_mpd_url) : url;
        dbg("switch to xgplayer-shaka DRM", { url: drmURL, drm });
        noAudioRetryTriedRef.current = false;
        const host = resolvePlayerHost();
        if (!host) throw new Error("player mount missing");
        host.innerHTML = "";
        const drmOptions: any = {
          id: domId,
          url: drmURL,
          fluid: false,
          width: "100%",
          height: "100%",
          autoplay: true,
          playsinline: true,
          pip: true,
          ...(startSec > 0 ? { startTime: startSec } : {}),
          plugins: [ShakaPlugin],
          shakaPlugin: {
            drm: {
              servers: {} as Record<string, string>,
            },
          },
        };
        if (drm.widevine_license_url) {
          drmOptions.shakaPlugin.drm.servers["com.widevine.alpha"] = drm.widevine_license_url;
        }
        if (drm.fairplay_license_url) {
          drmOptions.shakaPlugin.drm.servers["com.apple.fps"] = drm.fairplay_license_url;
        }
        playerRef.current = new Player(drmOptions);
        setLoadingText("");
        return;
      }

      // Clear progressive / AES-128 HLS: xgplayer (+ hls.js when needed).
      const useXgHlsPlugin =
        planMode === "hls_aes_128" || /\.m3u8(\?|#|$)/i.test(url) || /\/jit\/master\//i.test(url);
      const options: any = {
        id: domId,
        url,
        fluid: false,
        width: "100%",
        height: "100%",
        autoplay: true,
        playsinline: true,
        pip: true,
        screenShot: true,
        ...(startSec > 0 ? { startTime: startSec } : {}),
      };
      if (preview?.enabled && preview.status === "ready" && preview.thumbnail) {
        options.thumbnail = preview.thumbnail;
      }
      if (textTrackList.length > 0) {
        options.plugins = [TextTrack];
        options.texttrack = {
          list: textTrackList,
          isDefaultOpen: textTrackDefaultOpen,
          style: buildXgTexttrackStyle(playerPrefs.subtitle_appearance),
        };
      }
      if (useXgHlsPlugin) {
        // JIT master: do not pre-fetch the same URL again — /jit/master can block on first slice for minutes,
        // which left the overlay on "正在准备播放…" with no backend error. xgplayer-hls loads the master URL directly.
        const isJitMaster = /\/jit\/(master|session)\//i.test(url);
        const definitionList = isJitMaster ? [] : await fetchHlsDefinitions(url);
        if (definitionList.length > 0) {
          options.definition = {
            list: [{ definition: "auto", text: t("pages.player.definition_auto"), url }, ...definitionList],
            defaultDefinition: "auto",
          };
        }
        options.plugins = [...(options.plugins || []), HlsPlugin];
      }
      playerRef.current = new Player(options);
      const xg = playerRef.current;
      if (!xg) throw new Error("xgplayer init failed");
      const applySubtitleLook = () => {
        const root = (xg as { root?: HTMLElement }).root;
        applyKnoxSubtitleCssVars(root ?? null, playerPrefs.subtitle_appearance);
      };
      applySubtitleLook();
      xg.on("resize", applySubtitleLook);
      window.setTimeout(applySubtitleLook, 80);
      window.setTimeout(applySubtitleLook, 500);
      dbg("xgplayer init", { url, useXgHlsPlugin });
      xg.on("error", () => {
        dbgErr("xgplayer error event", { mid, url });
        if (sourceFallbackTriedRef.current || !mid) return;
        sourceFallbackTriedRef.current = true;
        setTranscodeStatus(null);
        setLoadingText(t("pages.player.device_unsupported_fallback"));
        const sourceURL = appendToken(`/api/v1/media/${mid}/play?prefer_source=1`);
        void fetchPreviewPlan().then(async (previewPlan) => {
          await playWithURL(sourceURL, previewPlan);
        });
      });
      const reportProgress = (completed = 0) => {
        if (!mid || !playerRef.current) return;
        const player = playerRef.current as any;
        const dur = safeSeconds(player.duration || 0);
        if (dur !== null && dur > 0) mediaDurationSecRef.current = dur;
        const cur = safeSeconds(player.currentTime || 0);
        if (cur === null) return;
        if (!completed && cur <= 0) return;
        lastProgressSecRef.current = cur;
        lastProgressAtRef.current = Date.now();
        if (completed) {
          void savePlaybackProgress(mid, withPlaybackLog({ position: cur, completed })).catch(() => {});
          return;
        }
        const now = Date.now();
        if (now - lastProgressSaveAtRef.current < 9000) return;
        lastProgressSaveAtRef.current = now;
        void savePlaybackProgress(mid, withPlaybackLog({ position: cur, completed })).catch(() => {});
      };
      xg.on("play", () => {
        if (playbackStartedRef.current) return;
        playbackStartedRef.current = true;
        if (!mid) return;
        const cur = safeSeconds((playerRef.current as any)?.currentTime || 0);
        if (cur === null) return;
        void reportPlaybackStart(mid, withPlaybackLog({ position: cur, completed: 0 })).catch(() => {});
      });
      xg.on("timeupdate", () => reportProgress(0));
      xg.on("ended", () => {
        if (isStale() || playbackEndedRef.current || !mid) return;
        reportProgress(1);
        handleMediaPlaybackComplete();
      });
      setLoadingText("");
    };
    const appendToken = (url: string) => {
      if (!url) return url;
      let out = url;
      if (!out.includes("access_token=")) {
        out = `${out}${out.includes("?") ? "&" : "?"}access_token=${encodeURIComponent(token)}`;
      }
      if (parentalUnlockToken && !out.includes("parental_unlock=")) {
        out = `${out}${out.includes("?") ? "&" : "?"}parental_unlock=${encodeURIComponent(parentalUnlockToken)}`;
      }
      return out;
    };
    const appendQueryValue = (raw: string, key: string, value: string) => {
      try {
        const u = new URL(raw, window.location.origin);
        if (!u.searchParams.get(key)) u.searchParams.set(key, value);
        return u.toString();
      } catch {
        const sep = raw.includes("?") ? "&" : "?";
        return `${raw}${sep}${encodeURIComponent(key)}=${encodeURIComponent(value)}`;
      }
    };
    const pollTaskStatus = async (taskId: number, fallback?: string) => {
      dbg("pollTaskStatus", { taskId, fallback });
      const statusResp = await fetch(`/api/v1/transcode/task/${taskId}/status?access_token=${encodeURIComponent(token)}`);
      if (!statusResp.ok) throw new Error(`task status failed: ${statusResp.status}`);
      const state = (await statusResp.json()) as TaskStatus;
      dbg("task state", state);
      if (isStale()) return;
      if (state.ready && state.hls_master) {
        setTranscodeStatus(null);
        setTranscodeProgress(100);
        const preview = await fetchPreviewPlan();
        const meta = playbackPlanMetaRef.current;
        await playWithURL(appendToken(state.hls_master), preview, {
          engineOrder: meta.engineOrder,
          planMode: meta.planMode ?? "hls",
          powerPlayerCfg: powerPlayerPlanRef.current,
        });
        return;
      }
      if (state.failed) {
        const fallbackURL = appendToken(fallback || `/api/v1/media/${mid}/play`);
        setTranscodeStatus(null);
        setLoadingText(t("pages.player.transcode_failed_fallback"));
        const preview = await fetchPreviewPlan();
        const meta = playbackPlanMetaRef.current;
        await playWithURL(fallbackURL, preview, {
          engineOrder: meta.engineOrder,
          planMode: "native",
          powerPlayerCfg: powerPlayerPlanRef.current,
        });
        return;
      }
      const progress = Number.isFinite(state.progress) ? Math.max(0, Math.min(99, state.progress || 0)) : 0;
      if (state.status === "waiting" || state.status === "running") {
        setTranscodeStatus(state.status);
      }
      setTranscodeProgress(progress);
      setLoadingText(t("pages.player.transcoding_progress", { percent: progress }));
      const nextDelay = state.poll_after_ms && state.poll_after_ms > 0 ? state.poll_after_ms : 1800;
      timer = window.setTimeout(() => {
        void pollTaskStatus(taskId, fallback);
      }, nextDelay);
    };
    const resolvePlan = async () => {
      jitPlaybackSessionIdRef.current = null;
      const caps = await capsPromise;
      const query = new URLSearchParams({
        access_token: token,
        video_codecs: caps.videoCodecs.join(","),
        audio_codecs: caps.audioCodecs.join(","),
        max_height: String(caps.maxHeight),
        qualities: caps.qualities.join(","),
        containers: caps.containers.join(","),
        mcap: caps.mcap,
      });
      const resp = await fetchWithTimeout(`/api/v1/media/${mid}/hls?${query.toString()}`, {}, 60_000);
      if (resp.status === 403) {
        let errBody: { error?: string } | null = null;
        try {
          errBody = (await resp.json()) as { error?: string };
        } catch {
          errBody = null;
        }
        const errStr = String(errBody?.error || "");
        if (errStr.includes("parental pin required")) {
          const pin = await new Promise<string>((resolve) => {
            const id = `parental-pin-${Date.now()}`;
            Modal.confirm({
              title: t("pages.player.pin_prompt_title"),
              content: <Input.Password id={id} placeholder={t("pages.player.pin_prompt_placeholder")} autoFocus />,
              onOk: () => {
                const el = document.getElementById(id) as HTMLInputElement | null;
                resolve((el?.value || "").trim());
              },
              onCancel: () => resolve(""),
            });
          });
          if (!pin) {
            throw new Error(t("pages.player.pin_required_play"));
          }
          const unlockResp = await fetch("/api/v1/user/parental/unlock", {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              Authorization: `Bearer ${token}`,
            },
            body: JSON.stringify({ media_id: mid, pin }),
          });
          if (!unlockResp.ok) {
            throw new Error(t("pages.player.pin_verify_failed"));
          }
          const unlock = (await unlockResp.json()) as { unlock_token?: string };
          if (!unlock.unlock_token) {
            throw new Error(t("pages.player.parental_unlock_failed"));
          }
          setParentalUnlockToken(unlock.unlock_token);
          message.success(t("pages.player.parental_unlocked"));
          timer = window.setTimeout(() => void resolvePlan(), 10);
          return;
        }
        throw new PlaybackPermissionError(playbackForbiddenMessage(errStr));
      }
      if (!resp.ok) throw new Error(`playback plan failed: ${resp.status}`);
      if (isStale()) return;
      const plan = (await resp.json()) as PlaybackPlan;
      powerPlayerPlanRef.current = plan.powerplayer;
      playbackPlanMetaRef.current = {
        engineOrder: coalesceEngineOrder(plan),
        planMode: plan.mode,
      };
      dbg("playback plan", plan);
      if (
        plan.mode === "hls" ||
        plan.mode === "hls_drm" ||
        plan.mode === "hls_aes_128" ||
        plan.mode === "hls_powerdrm" ||
        plan.mode === "jit_hls"
      ) {
        // JIT HLS (clear or stream DRM): scheduler serves master playlist; per-segment transcode on the fly.
        if (
          (plan.mode === "jit_hls" || plan.stream_drm) &&
          plan.hls_master
        ) {
          if (isStale()) return;
          const sid = typeof plan.session_id === "string" ? plan.session_id.trim() : "";
          jitPlaybackSessionIdRef.current = sid || null;
          setLoadingText(t("pages.player.connecting_jit"));
          const preview = await fetchPreviewPlan();
          const drmPayload =
            plan.mode === "hls_drm" || plan.mode === "hls_powerdrm" ? plan.drm : undefined;
          await playWithURL(appendToken(plan.hls_master), preview, {
            drm: drmPayload,
            engineOrder: coalesceEngineOrder(plan),
            planMode: plan.mode,
            powerPlayerCfg: plan.powerplayer,
          });
          return;
        }
        if (plan.status === "done" && plan.hls_master) {
          const preview = await fetchPreviewPlan();
          const drmPayload =
            plan.mode === "hls_drm" || plan.mode === "hls_powerdrm" ? plan.drm : undefined;
          await playWithURL(appendToken(plan.hls_master), preview, {
            drm: drmPayload,
            engineOrder: coalesceEngineOrder(plan),
            planMode: plan.mode,
            powerPlayerCfg: plan.powerplayer,
          });
          return;
        }
        if (plan.task_id && plan.task_id > 0) {
          setTranscodeStatus(plan.status === "running" ? "running" : "waiting");
          setTranscodeProgress(0);
          await pollTaskStatus(plan.task_id, plan.fallback);
          return;
        }
        setLoadingText(t("pages.player.preparing_transcode"));
        timer = window.setTimeout(() => void resolvePlan(), 1200);
        return;
      }
      const nativeURL = appendToken(plan.playUrl || `/api/v1/media/${mid}/play`);
      const preview = await fetchPreviewPlan();
      await playWithURL(nativeURL, preview, {
        engineOrder: coalesceEngineOrder(plan),
        planMode: plan.mode,
        powerPlayerCfg: plan.powerplayer,
        mimeType: plan.mime_type,
      });
    };
    void resolvePlan().catch((err: unknown) => {
      if (isStale()) return;
      if (err instanceof PlaybackPermissionError) {
        setTranscodeStatus(null);
        setLoadingText(err.message);
        message.error(err.message);
        return;
      }
      dbgErr("resolvePlan failed; fallback to source", err);
      setTranscodeStatus(null);
      setLoadingText(t("pages.player.play_prep_failed"));
      void fetchPreviewPlan().then(async (preview) => {
        try {
          await playWithURL(appendToken(`/api/v1/media/${mid}/play`), preview);
        } catch (e) {
          dbgErr("fallback playWithURL failed", e);
        }
      });
    });
    return () => {
      playbackGenerationRef.current++;
      if (mid && playbackStartedRef.current && !playbackEndedRef.current) {
        playbackEndedRef.current = true;
        const cur = safeSeconds((playerRef.current as any)?.currentTime || 0) ?? 0;
        void savePlaybackProgress(mid, withPlaybackLog({ position: cur, completed: 0 })).catch(() => {});
        void reportPlaybackEnd(mid, withPlaybackLog({ position: cur, completed: 0 })).catch(() => {});
      }
      if (timer) {
        window.clearTimeout(timer);
      }
      playerRef.current?.destroy();
      playerRef.current = null;
      void destroyDRMPlayer();
      void destroyPowerPlayer();
    };
  }, [mid, domId, token, startSec, parentalUnlockToken, canPlay]);

  useEffect(() => {
    return () => {
      if (hideTimerRef.current) {
        window.clearTimeout(hideTimerRef.current);
      }
    };
  }, []);

  const revealBack = () => {
    setShowBack(true);
    if (hideTimerRef.current) {
      window.clearTimeout(hideTimerRef.current);
    }
    hideTimerRef.current = window.setTimeout(() => setShowBack(false), 1800);
  };

  const goBackFromPlayer = () => {
    const pid = searchParams.get("playlist_id");
    if (pid && mid && !Number.isNaN(mid)) {
      nav(buildPlaylistPageUrl(Number(pid), mid));
      return;
    }
    const sid = searchParams.get("series_id");
    if (sid && mid && !Number.isNaN(mid)) {
      nav(buildSeriesPageUrl(Number(sid), mid));
      return;
    }
    const aid = searchParams.get("album_id");
    if (aid && mid && !Number.isNaN(mid)) {
      nav(buildAlbumPageUrl(Number(aid)));
      return;
    }
    nav(-1);
  };

  return (
    <div
      style={{ width: "100%", height: "100%", position: "relative", background: "#000", overflow: "hidden" }}
      onMouseMove={revealBack}
    >
      {showBack ? (
        <div style={{ position: "absolute", top: 16, left: 16, zIndex: 20 }}>
          <Button
            type="text"
            icon={<ArrowLeftOutlined style={{ fontSize: 18, color: "#fff" }} />}
            onClick={() => goBackFromPlayer()}
            aria-label={t("pages.player.aria_back")}
            style={{
              width: 40,
              height: 40,
              borderRadius: 20,
              background: "rgba(0,0,0,0.45)",
            }}
          >
          </Button>
        </div>
      ) : null}
      {mid && !Number.isNaN(mid) ? (
        <>
          <div ref={playerMountRef} id={domId} style={{ width: "100%", height: "100%" }} />
          {loadingText ? (
            <div
              style={{
                position: "absolute",
                inset: 0,
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                color: "#bbb",
                background: "rgba(0,0,0,0.35)",
                pointerEvents: "none",
                zIndex: 5,
              }}
            >
              {transcodeStatus ? (
                <div
                  style={{
                    width: "min(520px, 86vw)",
                    borderRadius: 12,
                    background: "rgba(15,15,15,0.86)",
                    border: "1px solid rgba(255,255,255,0.15)",
                    padding: "18px 18px 14px",
                    boxShadow: "0 10px 28px rgba(0,0,0,0.35)",
                  }}
                >
                  <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 10 }}>
                    <div style={{ color: "#fff", fontSize: 15, fontWeight: 600 }}>{t("pages.player.transcoding_label")}</div>
                    <Tag color={transcodeStatus === "running" ? "processing" : "gold"}>
                      {transcodeStatus === "running" ? "running" : "waiting"}
                    </Tag>
                  </div>
                  <Progress
                    percent={transcodeProgress}
                    status="active"
                    strokeColor="#1677ff"
                    railColor="rgba(255,255,255,0.2)"
                    format={(p) => `${Math.max(0, Math.min(99, p || 0))}%`}
                  />
                  <div style={{ marginTop: 8, color: "#c9c9c9", fontSize: 12 }}>{loadingText}</div>
                </div>
              ) : (
                loadingText
              )}
            </div>
          ) : null}
          {playlistFinished ? (
            <div
              style={{
                position: "absolute",
                inset: 0,
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                background: "rgba(0,0,0,0.55)",
                zIndex: 10,
              }}
            >
              <div
                style={{
                  width: "min(420px, 88vw)",
                  borderRadius: 12,
                  background: "rgba(15,15,15,0.92)",
                  border: "1px solid rgba(255,255,255,0.12)",
                  padding: "28px 24px 22px",
                  textAlign: "center",
                  boxShadow: "0 10px 28px rgba(0,0,0,0.45)",
                }}
              >
                <div style={{ color: "#fff", fontSize: 18, fontWeight: 600, marginBottom: 8 }}>{t("pages.player.playlist_done_title")}</div>
                <div style={{ color: "#aaa", fontSize: 13, marginBottom: 20 }}>{t("pages.player.playlist_done_detail")}</div>
                <Button
                  type="primary"
                  onClick={() => {
                    const pid = searchParams.get("playlist_id");
                    if (pid && mid && !Number.isNaN(mid)) {
                      nav(buildPlaylistPageUrl(Number(pid), mid));
                      return;
                    }
                    nav(-1);
                  }}
                >
                  {t("pages.player.back_to_playlist")}
                </Button>
              </div>
            </div>
          ) : null}
          {seriesFinished ? (
            <div
              style={{
                position: "absolute",
                inset: 0,
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                background: "rgba(0,0,0,0.55)",
                zIndex: 10,
              }}
            >
              <div
                style={{
                  width: "min(420px, 88vw)",
                  borderRadius: 12,
                  background: "rgba(15,15,15,0.92)",
                  border: "1px solid rgba(255,255,255,0.12)",
                  padding: "28px 24px 22px",
                  textAlign: "center",
                  boxShadow: "0 10px 28px rgba(0,0,0,0.45)",
                }}
              >
                <div style={{ color: "#fff", fontSize: 18, fontWeight: 600, marginBottom: 8 }}>{t("pages.player.series_done_title")}</div>
                <div style={{ color: "#aaa", fontSize: 13, marginBottom: 20 }}>{t("pages.player.series_done_detail")}</div>
                <Button
                  type="primary"
                  onClick={() => {
                    const sid = searchParams.get("series_id");
                    if (sid && mid && !Number.isNaN(mid)) {
                      nav(buildSeriesPageUrl(Number(sid), mid));
                      return;
                    }
                    nav(-1);
                  }}
                >
                  {t("pages.player.back_to_series")}
                </Button>
              </div>
            </div>
          ) : null}
        </>
      ) : (
        <div style={{ color: "#bbb", padding: 24 }}>{t("pages.player.no_media_id_short")}</div>
      )}
    </div>
  );
}
