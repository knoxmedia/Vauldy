export type PlaybackEvidenceEvent = "start" | "progress" | "seek" | "ended";

export type PlaybackEvidencePayload = {
  position: number;
  event: PlaybackEvidenceEvent;
  session_id: string;
  sequence: number;
  jit_session_id?: string;
};

export type PlaybackProgressResult = {
  completed: boolean;
  auto_completed: boolean;
  effective_position: number;
  stale: boolean;
};

type SessionIdFactory = () => string;
type JITSessionIdGetter = () => string | null | undefined;
type CryptoCapabilities = {
  randomUUID?: () => string;
  getRandomValues?: (array: Uint8Array) => Uint8Array;
};

const MAX_ID_UTF8_BYTES = 128;
let fallbackIdCounter = 0;

function currentCrypto(): CryptoCapabilities | undefined {
  const root = typeof globalThis === "object" && globalThis ? globalThis : undefined;
  const value = root?.crypto;
  return value && typeof value === "object" ? value as unknown as CryptoCapabilities : undefined;
}

function uuidFromRandomValues(crypto: CryptoCapabilities): string | undefined {
  if (typeof crypto.getRandomValues !== "function") return undefined;
  const bytes = new Uint8Array(16);
  try {
    crypto.getRandomValues(bytes);
  } catch {
    return undefined;
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (value) => value.toString(16).padStart(2, "0"));
  return `${hex.slice(0, 4).join("")}-${hex.slice(4, 6).join("")}-${hex.slice(6, 8).join("")}-${hex.slice(8, 10).join("")}-${hex.slice(10).join("")}`;
}

function defaultSessionIdFactory(): string {
  const crypto = currentCrypto();
  if (typeof crypto?.randomUUID === "function") {
    try {
      return crypto.randomUUID.call(crypto);
    } catch {
      // Continue through progressively weaker fallbacks.
    }
  }
  const secureUUID = crypto ? uuidFromRandomValues(crypto) : undefined;
  if (secureUUID) return secureUUID;

  // Playback IDs are correlation tokens, not secrets. This keeps non-secure LAN
  // contexts usable when Web Crypto is unavailable while reducing collisions.
  fallbackIdCounter += 1;
  return `play-${Date.now().toString(36)}-${fallbackIdCounter.toString(36)}-${Math.random().toString(36).slice(2)}`;
}

function hasOnlyUnicodeScalars(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
}

function utf8ByteLength(value: string): number {
  const root = typeof globalThis === "object" && globalThis ? globalThis : undefined;
  if (typeof root?.TextEncoder === "function") return new root.TextEncoder().encode(value).length;
  let bytes = 0;
  for (const scalar of value) {
    const code = scalar.codePointAt(0) ?? 0;
    bytes += code <= 0x7f ? 1 : code <= 0x7ff ? 2 : code <= 0xffff ? 3 : 4;
  }
  return bytes;
}

function normalizeBoundedId(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  if (!trimmed || !hasOnlyUnicodeScalars(trimmed)) return undefined;
  return utf8ByteLength(trimmed) <= MAX_ID_UTF8_BYTES ? trimmed : undefined;
}

function requireSessionId(value: unknown): string {
  if (typeof value !== "string" || !value.trim()) {
    throw new Error("Playback evidence requires a non-empty session ID");
  }
  const trimmed = value.trim();
  if (!hasOnlyUnicodeScalars(trimmed)) {
    throw new Error("Playback evidence session ID must be a valid Unicode scalar sequence");
  }
  if (utf8ByteLength(trimmed) > MAX_ID_UTF8_BYTES) {
    throw new Error("Playback evidence session ID must not exceed 128 UTF-8 bytes");
  }
  return trimmed;
}

function readJITSessionId(getJITSessionId?: JITSessionIdGetter): string | undefined {
  if (!getJITSessionId) return undefined;
  try {
    return normalizeBoundedId(getJITSessionId());
  } catch {
    return undefined;
  }
}

function normalizePosition(position: number): number {
  if (Number.isNaN(position) || position <= 0) return 0;
  if (!Number.isFinite(position) || position >= Number.MAX_SAFE_INTEGER) {
    return Number.MAX_SAFE_INTEGER;
  }
  return Math.floor(position);
}

export function createPlaybackEvidenceReporter(
  createSessionId: SessionIdFactory = defaultSessionIdFactory,
  getJITSessionId?: JITSessionIdGetter,
): Readonly<{ event: (event: PlaybackEvidenceEvent, position: number) => PlaybackEvidencePayload }> {
  const sessionId = requireSessionId(createSessionId());
  let sequence = 0;

  return Object.freeze({
    event(event: PlaybackEvidenceEvent, position: number): PlaybackEvidencePayload {
      if (sequence >= Number.MAX_SAFE_INTEGER) {
        throw new RangeError("Playback evidence sequence exhausted");
      }
      const jitSessionId = readJITSessionId(getJITSessionId);
      const nextSequence = sequence + 1;
      const payload: PlaybackEvidencePayload = {
        position: normalizePosition(position),
        event,
        session_id: sessionId,
        sequence: nextSequence,
        ...(jitSessionId ? { jit_session_id: jitSessionId } : {}),
      };
      sequence = nextSequence;
      return payload;
    },
  });
}