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
import { fetchMediaSubtitles, type MediaSubtitleRow, reportPlaybackEnd, reportPlaybackStart, savePlaybackProgress } from "../api/client";

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

function langLabel(code: string) {
  const c = (code || "").toLowerCase();
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
}

function kindLabel(kind: string) {
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
}

function buildTextTrackList(mediaId: number, token: string, rows: MediaSubtitleRow[]) {
  const ready = rows.filter((r) => r.status === "ready");
  return ready.map((r) => {
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
}

/** Maps backend `requireMediaAccess` / play handler error codes to user-readable text. */
function playbackForbiddenMessage(code: string): string {
  const c = (code || "").trim().toLowerCase();
  switch (c) {
    case "playback denied":
      return "当前账号未开通播放权限，请联系管理员在「用户管理 → 基本属性 → 操作权限」中开启「播放」。";
    case "library access denied":
      return "无权播放：该内容所属媒体库不在您的访问范围内。";
    case "folder access denied":
      return "无权播放：该文件所在文件夹未对您共享，请联系管理员检查文件夹权限。";
    case "outside parental allowed time":
      return "当前不在家长控制允许的观看时段内。";
    case "parental pin required":
      return "需要家长 PIN 才能播放此分级内容。";
    default:
      if (!code) return "播放被拒绝（403），请重新登录或联系管理员。";
      return `播放被拒绝：${code}`;
  }
}

class PlaybackPermissionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "PlaybackPermissionError";
  }
}

function detectClientCaps() {
  const probe = document.createElement("video");
  const supports = (mime: string) => {
    try {
      return probe.canPlayType(mime) !== "";
    } catch {
      return false;
    }
  };
  const videoCodecs: string[] = [];
  if (supports('video/mp4; codecs="avc1.42E01E"')) videoCodecs.push("h264");
  if (supports('video/mp4; codecs="hvc1.1.6.L93.B0"') || supports('video/mp4; codecs="hev1.1.6.L93.B0"')) videoCodecs.push("h265");
  if (supports('video/mp4; codecs="av01.0.05M.08"') || supports('video/webm; codecs="av1"')) videoCodecs.push("av1");

  const audioCodecs: string[] = [];
  if (supports('audio/mp4; codecs="mp4a.40.2"')) audioCodecs.push("aac");
  if (supports('audio/mpeg')) audioCodecs.push("mp3");
  if (supports('audio/webm; codecs="opus"')) audioCodecs.push("opus");

  const maxHeight = Math.max(360, Math.min(1080, window.screen?.height || 1080));
  const qualities = ["360p", "480p", "720p", "1080p"].filter((q) => parseInt(q, 10) <= maxHeight);
  return { videoCodecs, audioCodecs, maxHeight, qualities };
}

export default function PlayerPage() {
  const { id } = useParams();
  const [searchParams] = useSearchParams();
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
  const [loadingText, setLoadingText] = useState("正在准备播放...");
  const [parentalUnlockToken, setParentalUnlockToken] = useState<string>("");
  const [transcodeProgress, setTranscodeProgress] = useState<number>(0);
  const [transcodeStatus, setTranscodeStatus] = useState<"waiting" | "running" | null>(null);
  const playbackStartedRef = useRef(false);
  const playbackEndedRef = useRef(false);
  const lastProgressSecRef = useRef(0);
  const lastProgressAtRef = useRef(0);
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
    if (!mid || Number.isNaN(mid)) {
      setLoadingText("缺少媒体 ID，无法播放。");
      return;
    }
    if (!token) {
      setLoadingText("等待登录…");
      return;
    }
    if (canPlay === false) {
      setTranscodeStatus(null);
      setLoadingText("当前账号未开通播放权限");
      message.warning(
        "当前账号未开通播放权限。请在「用户管理」中为该用户开启「播放」，或联系管理员处理。"
      );
      return;
    }
    const sessionGen = ++playbackGenerationRef.current;
    const isStale = () => playbackGenerationRef.current !== sessionGen;
    jitPlaybackSessionIdRef.current = null;
    let timer: number | null = null;
    const withPlaybackLog = <T extends Record<string, unknown>>(p: T): T & { session_id?: string } => {
      const sid = jitPlaybackSessionIdRef.current?.trim();
      if (!sid) return p;
      return { ...p, session_id: sid };
    };
    const caps = detectClientCaps();
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
        const pos = readPowerPlayerTimePayload(event);
        if (pos === null) return;
        const cur = safeSeconds(pos);
        if (cur === null || cur <= 0) return;
        const now = Date.now();
        if (cur <= lastProgressSecRef.current && now - lastProgressAtRef.current < 9000) return;
        if (now - lastProgressAtRef.current < 9000) return;
        lastProgressSecRef.current = cur;
        lastProgressAtRef.current = now;
        void savePlaybackProgress(mid, withPlaybackLog({ position: cur, completed: 0 })).catch(() => {});
        if (!playbackStartedRef.current) {
          playbackStartedRef.current = true;
          void reportPlaybackStart(mid, withPlaybackLog({ position: cur, completed: 0 })).catch(() => {});
        }
      });

      bind("onComplete", () => {
        if (isStale() || !mid || playbackEndedRef.current) return;
        playbackEndedRef.current = true;
        dbg("powerplayer onComplete");
        const endPos = lastProgressSecRef.current;
        void savePlaybackProgress(mid, withPlaybackLog({ position: endPos, completed: 1 })).catch(() => {});
        void reportPlaybackEnd(mid, withPlaybackLog({ position: endPos, completed: 1 })).catch(() => {});
      });

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
        setLoadingText("DRM 播放失败，正在回退到源文件播放...");
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
        const now = Date.now();
        const cur = safeSeconds(video.currentTime);
        if (cur === null) return;
        if (cur <= 0) return;
        if (cur <= lastProgressSecRef.current && now - lastProgressAtRef.current < 9000) return;
        if (now - lastProgressAtRef.current < 9000) return;
        lastProgressSecRef.current = cur;
        lastProgressAtRef.current = now;
        void savePlaybackProgress(mid, withPlaybackLog({ position: cur, completed: 0 })).catch(() => {});
      });
      video.addEventListener("ended", () => {
        if (playbackEndedRef.current || !mid) return;
        playbackEndedRef.current = true;
        const cur = safeSeconds(video.currentTime) ?? 0;
        void savePlaybackProgress(mid, withPlaybackLog({ position: cur, completed: 1 })).catch(() => {});
        void reportPlaybackEnd(mid, withPlaybackLog({ position: cur, completed: 1 })).catch(() => {});
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
          throw new Error(
            "未加载 PowerPlayer，无法播放 PowerDRM 内容。请将 powerplayer.min.js 部署到 /static/powerplayer6/ 并刷新页面。"
          );
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
      let textTrackList: ReturnType<typeof buildTextTrackList> = [];
      if (mid) {
        try {
          const rows = await withTimeout(fetchMediaSubtitles(mid), 12_000, "subtitles");
          textTrackList = buildTextTrackList(mid, token, rows);
        } catch {
          textTrackList = [];
        }
      }
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
          isDefaultOpen: false,
        };
      }
      if (useXgHlsPlugin) {
        // JIT master: do not pre-fetch the same URL again — /jit/master can block on first slice for minutes,
        // which left the overlay on "正在准备播放…" with no backend error. xgplayer-hls loads the master URL directly.
        const isJitMaster = /\/jit\/(master|session)\//i.test(url);
        const definitionList = isJitMaster ? [] : await fetchHlsDefinitions(url);
        if (definitionList.length > 0) {
          options.definition = {
            list: [{ definition: "auto", text: "自动", url }, ...definitionList],
            defaultDefinition: "auto",
          };
        }
        options.plugins = [...(options.plugins || []), HlsPlugin];
      }
      playerRef.current = new Player(options);
      const xg = playerRef.current;
      if (!xg) throw new Error("xgplayer init failed");
      dbg("xgplayer init", { url, useXgHlsPlugin });
      xg.on("error", () => {
        dbgErr("xgplayer error event", { mid, url });
        if (sourceFallbackTriedRef.current || !mid) return;
        sourceFallbackTriedRef.current = true;
        setTranscodeStatus(null);
        setLoadingText("当前设备不支持该流，正在回退到源文件播放...");
        const sourceURL = appendToken(`/api/v1/media/${mid}/play?prefer_source=1`);
        void fetchPreviewPlan().then(async (previewPlan) => {
          await playWithURL(sourceURL, previewPlan);
        });
      });
      const reportProgress = (completed = 0) => {
        if (!mid || !playerRef.current) return;
        const now = Date.now();
        const cur = safeSeconds((playerRef.current as any).currentTime || 0);
        if (cur === null) return;
        if (!completed && cur <= 0) return;
        if (!completed) {
          if (cur <= lastProgressSecRef.current && now - lastProgressAtRef.current < 9000) return;
          if (now - lastProgressAtRef.current < 9000) return;
        }
        lastProgressSecRef.current = cur;
        lastProgressAtRef.current = now;
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
        if (playbackEndedRef.current) return;
        playbackEndedRef.current = true;
        if (!mid) return;
        const cur = safeSeconds((playerRef.current as any)?.currentTime || 0) ?? 0;
        reportProgress(1);
        void reportPlaybackEnd(mid, withPlaybackLog({ position: cur, completed: 1 })).catch(() => {});
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
        setLoadingText("转码失败，正在回退到原始播放...");
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
      setLoadingText(`正在实时转码为 HLS 自适应流（${progress}%），请稍候...`);
      const nextDelay = state.poll_after_ms && state.poll_after_ms > 0 ? state.poll_after_ms : 1800;
      timer = window.setTimeout(() => {
        void pollTaskStatus(taskId, fallback);
      }, nextDelay);
    };
    const resolvePlan = async () => {
      jitPlaybackSessionIdRef.current = null;
      const query = new URLSearchParams({
        access_token: token,
        video_codecs: caps.videoCodecs.join(","),
        audio_codecs: caps.audioCodecs.join(","),
        max_height: String(caps.maxHeight),
        qualities: caps.qualities.join(","),
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
              title: "请输入家长 PIN",
              content: <Input.Password id={id} placeholder="家长 PIN" autoFocus />,
              onOk: () => {
                const el = document.getElementById(id) as HTMLInputElement | null;
                resolve((el?.value || "").trim());
              },
              onCancel: () => resolve(""),
            });
          });
          if (!pin) {
            throw new Error("需要家长 PIN 才能播放");
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
            throw new Error("家长 PIN 验证失败");
          }
          const unlock = (await unlockResp.json()) as { unlock_token?: string };
          if (!unlock.unlock_token) {
            throw new Error("家长控制解锁失败");
          }
          setParentalUnlockToken(unlock.unlock_token);
          message.success("已临时解锁受限内容");
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
        // JIT HLS: scheduler serves master playlist; per-segment transcode on the fly.
        if (plan.mode === "jit_hls" && plan.hls_master) {
          if (isStale()) return;
          const sid = typeof plan.session_id === "string" ? plan.session_id.trim() : "";
          jitPlaybackSessionIdRef.current = sid || null;
          setLoadingText("正在连接即时播放…");
          const preview = await fetchPreviewPlan();
          await playWithURL(appendToken(plan.hls_master), preview, {
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
        setLoadingText("正在准备转码任务，请稍候...");
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
      setLoadingText("播放准备失败，正在尝试原始播放...");
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
            onClick={() => nav(-1)}
            aria-label="返回上一页"
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
                    <div style={{ color: "#fff", fontSize: 15, fontWeight: 600 }}>正在转码</div>
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
        </>
      ) : (
        <div style={{ color: "#bbb", padding: 24 }}>缺少媒体 ID，无法播放。</div>
      )}
    </div>
  );
}
