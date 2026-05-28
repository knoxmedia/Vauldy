import {
  createContext,
  createElement,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import { useAuthStore } from "../store/auth";

// Eagerly load all locale JSON files; new files dropped into ./locales appear
// in the language dropdowns automatically per NFR-02.
const localeModules = import.meta.glob<Record<string, unknown>>("./locales/*.json", {
  eager: true,
  import: "default",
});

export type LocaleResource = Record<string, unknown>;

/** Map of locale code -> dictionary. */
const RESOURCES: Record<string, LocaleResource> = (() => {
  const out: Record<string, LocaleResource> = {};
  for (const [path, dict] of Object.entries(localeModules)) {
    const match = /\.\/locales\/([^/]+)\.json$/.exec(path);
    if (!match) continue;
    out[match[1]!] = (dict ?? {}) as LocaleResource;
  }
  return out;
})();

/** Supported locale codes detected from the JSON files (sorted). */
export const SUPPORTED_LOCALES: readonly string[] = Object.keys(RESOURCES).sort((a, b) => {
  // Keep zh-CN first, then zh-TW, then alphabetical.
  const order = ["zh-CN", "zh-TW", "en", "ja", "ko"];
  const ia = order.indexOf(a);
  const ib = order.indexOf(b);
  if (ia !== -1 && ib !== -1) return ia - ib;
  if (ia !== -1) return -1;
  if (ib !== -1) return 1;
  return a.localeCompare(b);
});

export const DEFAULT_LOCALE = "zh-CN";
export const FALLBACK_LOCALE = "en";

const LOCAL_STORAGE_KEY = "knox-media-ui-locale";

/** Normalize a free-form locale value to one of the supported codes. */
export function resolveLocale(input: string | null | undefined): string {
  const raw = (input || "").trim();
  if (!raw) return DEFAULT_LOCALE;
  // Direct match.
  if (RESOURCES[raw]) return raw;
  // Case-insensitive match against known codes.
  const lower = raw.toLowerCase();
  for (const code of SUPPORTED_LOCALES) {
    if (code.toLowerCase() === lower) return code;
  }
  // Common legacy / alias mappings.
  const aliases: Record<string, string> = {
    zh: "zh-CN",
    "zh-hans": "zh-CN",
    "zh-hans-cn": "zh-CN",
    "zh-cn": "zh-CN",
    "zh-hant": "zh-TW",
    "zh-hant-tw": "zh-TW",
    "zh-tw": "zh-TW",
    "zh-hk": "zh-TW",
    en: "en",
    "en-us": "en",
    "en-gb": "en",
    ja: "ja",
    "ja-jp": "ja",
    ko: "ko",
    "ko-kr": "ko",
  };
  const alias = aliases[lower];
  if (alias && RESOURCES[alias]) return alias;
  // Primary subtag (e.g. en-US -> en).
  const primary = lower.split(/[-_]/)[0]!;
  for (const code of SUPPORTED_LOCALES) {
    if (code.toLowerCase().split("-")[0] === primary) return code;
  }
  return DEFAULT_LOCALE;
}

function lookup(resource: LocaleResource | undefined, key: string): string | undefined {
  if (!resource) return undefined;
  const parts = key.split(".");
  let cur: unknown = resource;
  for (const p of parts) {
    if (cur && typeof cur === "object" && p in (cur as Record<string, unknown>)) {
      cur = (cur as Record<string, unknown>)[p];
    } else {
      return undefined;
    }
  }
  return typeof cur === "string" ? cur : undefined;
}

function interpolate(template: string, params?: Record<string, string | number>): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (_, k) => {
    const v = params[k];
    return v === undefined || v === null ? "" : String(v);
  });
}

export type TranslateFn = (key: string, paramsOrFallback?: Record<string, string | number> | string, fallback?: string) => string;

export type I18nContextValue = {
  locale: string;
  setLocale: (next: string) => void;
  t: TranslateFn;
  supported: readonly string[];
};

const I18nContext = createContext<I18nContextValue | null>(null);

/**
 * Module-level active locale, kept in sync with the React provider state.
 * Allows non-React callers (helpers, menus, action builders) to look up
 * translations without threading a t() function through every signature.
 */
let activeLocale: string = DEFAULT_LOCALE;

/** Translate using the current active locale. Safe to call outside React. */
export function tGlobal(
  key: string,
  paramsOrFallback?: Record<string, string | number> | string,
  fallback?: string
): string {
  const params =
    paramsOrFallback && typeof paramsOrFallback === "object" ? paramsOrFallback : undefined;
  const inlineFallback = typeof paramsOrFallback === "string" ? paramsOrFallback : fallback;
  const primary = lookup(RESOURCES[activeLocale], key);
  if (primary !== undefined) return interpolate(primary, params);
  const fb = lookup(RESOURCES[FALLBACK_LOCALE], key);
  if (fb !== undefined) return interpolate(fb, params);
  if (inlineFallback !== undefined) return interpolate(inlineFallback, params);
  return key;
}

/** Return the current active locale code. */
export function getActiveLocale(): string {
  return activeLocale;
}

function readInitialLocale(): string {
  // Persisted user preference takes priority for instant first render before
  // /user/info resolves.
  try {
    const stored = useAuthStore.getState().uiLocale;
    if (stored) return resolveLocale(stored);
  } catch {
    /* ignore */
  }
  try {
    const cached = localStorage.getItem(LOCAL_STORAGE_KEY);
    if (cached) return resolveLocale(cached);
  } catch {
    /* ignore */
  }
  if (typeof navigator !== "undefined" && navigator.language) {
    return resolveLocale(navigator.language);
  }
  return DEFAULT_LOCALE;
}

export type I18nProviderProps = {
  children: ReactNode;
  /**
   * Optional controlled locale override (used e.g. by tests). When provided,
   * the context uses this exact locale without subscribing to the auth store.
   */
  locale?: string;
};

export function I18nProvider({ children, locale: locked }: I18nProviderProps) {
  const storeLocale = useAuthStore((s) => s.uiLocale);
  const [localeState, setLocaleState] = useState<string>(() => {
    const initial = locked ? resolveLocale(locked) : readInitialLocale();
    activeLocale = initial;
    return initial;
  });

  // React to changes in the auth store's uiLocale (e.g. after login or settings save).
  useEffect(() => {
    if (locked) return;
    if (!storeLocale) return;
    const next = resolveLocale(storeLocale);
    setLocaleState((prev) => (prev === next ? prev : next));
  }, [storeLocale, locked]);

  useEffect(() => {
    activeLocale = localeState;
    try {
      if (typeof document !== "undefined") {
        document.documentElement.lang = localeState;
      }
      localStorage.setItem(LOCAL_STORAGE_KEY, localeState);
    } catch {
      /* ignore */
    }
  }, [localeState]);

  const setLocale = useCallback((next: string) => {
    setLocaleState((prev) => {
      const resolved = resolveLocale(next);
      return prev === resolved ? prev : resolved;
    });
  }, []);

  const t = useCallback<TranslateFn>(
    (key, paramsOrFallback, fallback) => {
      const params =
        paramsOrFallback && typeof paramsOrFallback === "object" ? paramsOrFallback : undefined;
      const inlineFallback =
        typeof paramsOrFallback === "string" ? paramsOrFallback : fallback;
      const primary = lookup(RESOURCES[localeState], key);
      if (primary !== undefined) return interpolate(primary, params);
      const fb = lookup(RESOURCES[FALLBACK_LOCALE], key);
      if (fb !== undefined) return interpolate(fb, params);
      if (inlineFallback !== undefined) return interpolate(inlineFallback, params);
      return key;
    },
    [localeState]
  );

  const value = useMemo<I18nContextValue>(
    () => ({
      locale: localeState,
      setLocale,
      t,
      supported: SUPPORTED_LOCALES,
    }),
    [localeState, setLocale, t]
  );

  return createElement(I18nContext.Provider, { value }, children);
}

export function useI18n(): I18nContextValue {
  const ctx = useContext(I18nContext);
  if (!ctx) {
    // Fallback path when called outside the provider (e.g. unit tests).
    return {
      locale: DEFAULT_LOCALE,
      setLocale: () => {},
      t: (key, paramsOrFallback, fallback) => {
        const params =
          paramsOrFallback && typeof paramsOrFallback === "object" ? paramsOrFallback : undefined;
        const inlineFallback =
          typeof paramsOrFallback === "string" ? paramsOrFallback : fallback;
        const primary = lookup(RESOURCES[DEFAULT_LOCALE], key) ?? lookup(RESOURCES[FALLBACK_LOCALE], key);
        if (primary !== undefined) return interpolate(primary, params);
        if (inlineFallback !== undefined) return interpolate(inlineFallback, params);
        return key;
      },
      supported: SUPPORTED_LOCALES,
    };
  }
  return ctx;
}

export function useT(): TranslateFn {
  return useI18n().t;
}

/** Get the display label for a locale code in its own script. */
export function localeDisplayName(code: string): string {
  return (
    (lookup(RESOURCES[code], `languages.${code}`) as string | undefined) ||
    (lookup(RESOURCES[DEFAULT_LOCALE], `languages.${code}`) as string | undefined) ||
    code
  );
}

/** Build a list of { value, label } options for language dropdowns. */
export function languageOptions(): { value: string; label: string }[] {
  return SUPPORTED_LOCALES.map((code) => ({ value: code, label: localeDisplayName(code) }));
}
