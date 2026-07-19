import { describe, expect, it } from "vitest";

import zhCN from "../locales/zh-CN.json";
import zhTW from "../locales/zh-TW.json";
import en from "../locales/en.json";
import ja from "../locales/ja.json";
import ko from "../locales/ko.json";

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


describe("video optimization source hint", () => {
  it("states DB-record uncertainty and runtime validation in every locale", () => {
    const hints = [zhCN, zhTW, en, ja, ko].map(
      (locale) => locale.components.video_optimization_modal.plaintext_required,
    );
    expect(hints).toEqual([
      "\u672a\u8bb0\u5f55\u53ef\u7528\u7684\u6e90\u6587\u4ef6\uff1b\u5f00\u59cb\u4f18\u5316\u65f6\u4f1a\u9a8c\u8bc1\u6587\u4ef6\u3002",
      "\u672a\u8a18\u9304\u53ef\u7528\u7684\u4f86\u6e90\u6a94\u6848\uff1b\u958b\u59cb\u512a\u5316\u6642\u6703\u9a57\u8b49\u6a94\u6848\u3002",
      "No usable source is recorded; the file is validated when optimization starts.",
      "\u4f7f\u7528\u53ef\u80fd\u306a\u30bd\u30fc\u30b9\u306f\u8a18\u9332\u3055\u308c\u3066\u3044\u307e\u305b\u3093\u3002\u6700\u9069\u5316\u306e\u958b\u59cb\u6642\u306b\u30d5\u30a1\u30a4\u30eb\u3092\u691c\u8a3c\u3057\u307e\u3059\u3002",
      "\uc0ac\uc6a9 \uac00\ub2a5\ud55c \uc18c\uc2a4\uac00 \uae30\ub85d\ub418\uc5b4 \uc788\uc9c0 \uc54a\uc2b5\ub2c8\ub2e4. \ucd5c\uc801\ud654\ub97c \uc2dc\uc791\ud560 \ub54c \ud30c\uc77c\uc744 \uac80\uc99d\ud569\ub2c8\ub2e4.",
    ]);
  });
});


describe("media menu optimization source hint", () => {
  it("uses the recorded-candidate and runtime-validation wording in every locale", () => {
    const hints = [zhCN, zhTW, en, ja, ko].map(
      (locale) => locale.components.media_menu.optimize_unavailable_hint,
    );
    expect(hints).toEqual([
      "\u672a\u8bb0\u5f55\u53ef\u7528\u7684\u6e90\u6587\u4ef6\uff1b\u5f00\u59cb\u4f18\u5316\u65f6\u4f1a\u9a8c\u8bc1\u6587\u4ef6\u3002",
      "\u672a\u8a18\u9304\u53ef\u7528\u7684\u4f86\u6e90\u6a94\u6848\uff1b\u958b\u59cb\u512a\u5316\u6642\u6703\u9a57\u8b49\u6a94\u6848\u3002",
      "No usable source is recorded; the file is validated when optimization starts.",
      "\u4f7f\u7528\u53ef\u80fd\u306a\u30bd\u30fc\u30b9\u306f\u8a18\u9332\u3055\u308c\u3066\u3044\u307e\u305b\u3093\u3002\u6700\u9069\u5316\u306e\u958b\u59cb\u6642\u306b\u30d5\u30a1\u30a4\u30eb\u3092\u691c\u8a3c\u3057\u307e\u3059\u3002",
      "\uc0ac\uc6a9 \uac00\ub2a5\ud55c \uc18c\uc2a4\uac00 \uae30\ub85d\ub418\uc5b4 \uc788\uc9c0 \uc54a\uc2b5\ub2c8\ub2e4. \ucd5c\uc801\ud654\ub97c \uc2dc\uc791\ud560 \ub54c \ud30c\uc77c\uc744 \uac80\uc99d\ud569\ub2c8\ub2e4.",
    ]);
  });
});


describe("document batch tag translations", () => {
  it("uses exact native labels for Chinese and English", () => {
    expect(zhCN.pages.document_browse.batch_tags).toBe("\u6279\u91cf\u6807\u7b7e ({count})");
    expect(en.pages.document_browse.batch_tags).toBe("Tags ({count})");
  });

  it("has valid native text without replacement placeholders in every locale", () => {
    const groups = [zhCN, zhTW, en, ja, ko].map((locale) => locale.pages.document_browse);
    for (const group of groups) {
      for (const key of ["batch_tags", "batch_tags_title", "batch_tags_mode", "batch_tags_add", "batch_tags_remove", "batch_tags_replace", "batch_tags_input", "batch_tags_placeholder", "batch_tags_enter", "batch_tags_too_many", "batch_tags_updated", "batch_tags_failed"] as const) {
        const value = group[key];
        expect(value.trim()).not.toBe("");
        expect(value).not.toContain("??");
        expect(value).not.toContain("?");
      }
    }
    expect(ja.pages.document_browse.batch_tags_add).toBe("\u8ffd\u52a0");
    expect(ko.pages.document_browse.batch_tags_add).toBe("\ucd94\uac00");
  });
});
