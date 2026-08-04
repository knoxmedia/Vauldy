/**
 * Task control filter/query utilities for URL round-trip and filter hash comparison.
 */

export interface TaskControlFilter {
  task_type?: string;
  status?: string;
  source?: string;
  library_id?: number;
  generation?: number;
  capability?: string;
  owner?: string;
  blocker?: string;
  removed?: string;
}

const KNOWN_KEYS = new Set([
  "task_type", "status", "source", "library_id", "generation",
  "capability", "owner", "blocker", "removed",
]);

const NUMERIC_KEYS = new Set(["library_id", "generation"]);

/**
 * Converts a TaskControlFilter to a URL query string.
 * Undefined and 0 values are omitted.
 */
export function filterToQueryString(filter: TaskControlFilter): string {
  const parts: string[] = [];
  for (const [key, val] of Object.entries(filter)) {
    if (val === undefined || val === "") continue;
    if (NUMERIC_KEYS.has(key) && val === 0) continue;
    parts.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(val))}`);
  }
  return parts.join("&");
}

/**
 * Parses a URL query string (with or without leading '?') into a TaskControlFilter.
 * Unrecognized keys and empty values are silently ignored.
 */
export function queryToFilter(query: string): TaskControlFilter {
  const s = query.startsWith("?") ? query.slice(1) : query;
  if (!s) return {};

  const filter: TaskControlFilter = {};
  const params = new URLSearchParams(s);
  for (const [key, val] of params.entries()) {
    if (!KNOWN_KEYS.has(key)) continue;
    if (val === "") continue;
    if (NUMERIC_KEYS.has(key)) {
      const num = Number(val);
      if (!Number.isNaN(num) && num > 0) {
        (filter as Record<string, unknown>)[key] = num;
      }
    } else {
      (filter as Record<string, unknown>)[key] = val;
    }
  }
  return filter;
}

/**
 * Compares two query strings for filter equivalence by normalizing and sorting
 * their key-value pairs. Used for cursor preservation checks.
 */
export function filterHashMatches(qsA: string, qsB: string): boolean {
  const normA = normalizeQueryString(qsA);
  const normB = normalizeQueryString(qsB);
  return normA === normB;
}

function normalizeQueryString(query: string): string {
  const s = query.startsWith("?") ? query.slice(1) : query;
  if (!s) return "";

  const params = new URLSearchParams(s);
  params.sort();
  return params.toString();
}
