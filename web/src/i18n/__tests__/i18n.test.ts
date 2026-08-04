import { describe, expect, it } from "vitest";

import { resolveLocale, SUPPORTED_LOCALES, DEFAULT_LOCALE, languageOptions } from "../index";

describe("resolveLocale", () => {
  it("returns the default locale for empty input", () => {
    expect(resolveLocale("")).toBe(DEFAULT_LOCALE);
    expect(resolveLocale(undefined)).toBe(DEFAULT_LOCALE);
    expect(resolveLocale(null)).toBe(DEFAULT_LOCALE);
  });

  it("returns an exact match unchanged", () => {
    expect(resolveLocale("zh-CN")).toBe("zh-CN");
    expect(resolveLocale("en")).toBe("en");
    expect(resolveLocale("ja")).toBe("ja");
  });

  it("maps legacy aliases to the canonical code", () => {
    expect(resolveLocale("zh")).toBe("zh-CN");
    expect(resolveLocale("zh-Hans")).toBe("zh-CN");
    expect(resolveLocale("zh-Hant")).toBe("zh-TW");
    expect(resolveLocale("en-US")).toBe("en");
    expect(resolveLocale("ja-JP")).toBe("ja");
  });

  it("is case-insensitive on the way in", () => {
    expect(resolveLocale("ZH-cn")).toBe("zh-CN");
    expect(resolveLocale("EN")).toBe("en");
  });

  it("falls back to the default for unknown languages", () => {
    expect(resolveLocale("xx-YY")).toBe(DEFAULT_LOCALE);
  });
});

describe("supported locales", () => {
  it("includes every spec-mandated code (zh-CN, zh-TW, en, ja, ko)", () => {
    for (const code of ["zh-CN", "zh-TW", "en", "ja", "ko"]) {
      expect(SUPPORTED_LOCALES).toContain(code);
    }
  });

  it("languageOptions returns an entry per locale", () => {
    const opts = languageOptions();
    expect(opts.length).toBe(SUPPORTED_LOCALES.length);
    for (const opt of opts) {
      expect(opt.value).toBeTruthy();
      expect(opt.label).toBeTruthy();
    }
  });
});
