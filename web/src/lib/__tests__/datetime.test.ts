import { describe, expect, it } from "vitest";
import {
  formatServerDateTime,
  localDayKey,
  parseServerDateTime,
  serverDateTimeToMillis,
} from "../datetime";

describe("datetime", () => {
  it("treats space-separated datetimes as UTC", () => {
    const d = parseServerDateTime("2026-06-23 06:10:16");
    expect(d?.toISOString()).toBe("2026-06-23T06:10:16.000Z");
  });

  it("treats T-separated datetimes without timezone as UTC", () => {
    const d = parseServerDateTime("2026-06-23T06:10:16");
    expect(d?.toISOString()).toBe("2026-06-23T06:10:16.000Z");
  });

  it("parses explicit UTC suffix", () => {
    const d = parseServerDateTime("2026-06-23T06:10:16Z");
    expect(d?.toISOString()).toBe("2026-06-23T06:10:16.000Z");
  });

  it("converts UTC millis for sorting", () => {
    expect(serverDateTimeToMillis("2026-06-23 06:10:16")).toBe(Date.UTC(2026, 5, 23, 6, 10, 16));
  });

  it("formats empty values as dash", () => {
    expect(formatServerDateTime(undefined)).toBe("—");
    expect(formatServerDateTime("", { empty: "-" })).toBe("-");
  });

  it("groups by local calendar day", () => {
    const key = localDayKey("2026-06-23 06:10:16");
    const d = parseServerDateTime("2026-06-23 06:10:16")!;
    const expected = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
    expect(key).toBe(expected);
  });
});
