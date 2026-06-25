import { getActiveLocale } from "../i18n";

const HAS_TIMEZONE = /(Z|[+-]\d{2}:?\d{2})$/i;
const DATE_ONLY = /^\d{4}-\d{2}-\d{2}$/;

/** Parse API/SQLite timestamps (UTC, no suffix) into a Date in the client timezone. */
export function parseServerDateTime(value?: string | null): Date | null {
  const raw = (value ?? "").trim();
  if (!raw) return null;
  if (HAS_TIMEZONE.test(raw)) {
    const d = new Date(raw);
    return Number.isNaN(d.getTime()) ? null : d;
  }
  if (DATE_ONLY.test(raw)) {
    const d = new Date(`${raw}T00:00:00Z`);
    return Number.isNaN(d.getTime()) ? null : d;
  }
  const normalized = raw.includes("T") ? raw : raw.replace(" ", "T");
  const d = new Date(`${normalized}Z`);
  return Number.isNaN(d.getTime()) ? null : d;
}

export function serverDateTimeToMillis(value?: string | null): number {
  const d = parseServerDateTime(value);
  return d ? d.getTime() : 0;
}

export type FormatServerDateTimeOptions = {
  locale?: string;
  empty?: string;
};

/** Full date+time in the user's locale and local timezone. */
export function formatServerDateTime(
  value?: string | null,
  options?: FormatServerDateTimeOptions,
): string {
  const empty = options?.empty ?? "—";
  const d = parseServerDateTime(value);
  if (!d) return empty;
  const locale = options?.locale ?? getActiveLocale();
  return d.toLocaleString(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

/** Date portion only (for added-at style columns). */
export function formatServerDate(
  value?: string | null,
  options?: FormatServerDateTimeOptions,
): string {
  const empty = options?.empty ?? "—";
  const d = parseServerDateTime(value);
  if (!d) return empty;
  const locale = options?.locale ?? getActiveLocale();
  return d.toLocaleDateString(locale);
}

/** Ant Design Table cell renderer for server UTC timestamps. */
export function renderServerDateTime(value?: string | null): string {
  return formatServerDateTime(value);
}

/** Group photos/logs by calendar day in the client's local timezone. */
export function localDayKey(value?: string | null): string {
  const d = parseServerDateTime(value);
  if (!d) {
    const raw = (value ?? "").trim();
    return raw ? raw.slice(0, 10) : "unknown";
  }
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}
