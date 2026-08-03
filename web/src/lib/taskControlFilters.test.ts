import { describe, it, expect } from "vitest";
import {
  filterToQueryString,
  queryToFilter,
  filterHashMatches,
  type TaskControlFilter,
} from "./taskControlFilters";

describe("taskControlFilters", () => {
  describe("filterToQueryString and queryToFilter round-trip", () => {
    it("round-trips basic fields", () => {
      const filter: TaskControlFilter = {
        task_type: "poster",
        status: "running",
        removed: "exclude",
      };
      const qs = filterToQueryString(filter);
      const parsed = queryToFilter(qs);
      expect(parsed.task_type).toBe("poster");
      expect(parsed.status).toBe("running");
      expect(parsed.removed).toBe("exclude");
    });

    it("round-trips nullable numeric fields", () => {
      const filter: TaskControlFilter = {
        task_type: "transcode",
        library_id: 42,
        generation: 5,
      };
      const qs = filterToQueryString(filter);
      const parsed = queryToFilter(qs);
      expect(parsed.task_type).toBe("transcode");
      expect(parsed.library_id).toBe(42);
      expect(parsed.generation).toBe(5);
    });

    it("omits undefined values from query string", () => {
      const filter: TaskControlFilter = { task_type: "scan" };
      const qs = filterToQueryString(filter);
      expect(qs).not.toContain("status=");
      expect(qs).not.toContain("removed=");
    });

    it("omits 0 numeric values from query string", () => {
      const filter: TaskControlFilter = {
        task_type: "scan",
        library_id: 0,
      };
      const qs = filterToQueryString(filter);
      expect(qs).not.toContain("library_id=0");
    });

    it("handles empty filter", () => {
      const qs = filterToQueryString({});
      expect(qs).toBe("");
      const parsed = queryToFilter(qs);
      expect(Object.keys(parsed).length).toBe(0);
    });

    it("handles filter with only status", () => {
      const filter: TaskControlFilter = { status: "failed" };
      const qs = filterToQueryString(filter);
      expect(qs).toBe("status=failed");
    });

    it("handles removed modes: include, exclude, only", () => {
      for (const mode of ["exclude", "include", "only"] as const) {
        const filter: TaskControlFilter = { task_type: "poster", removed: mode };
        const qs = filterToQueryString(filter);
        expect(qs).toContain(`removed=${mode}`);
        const parsed = queryToFilter(qs);
        expect(parsed.removed).toBe(mode);
      }
    });
  });

  describe("filterHashMatches", () => {
    it("two identical filters produce matching hashes", () => {
      const f1: TaskControlFilter = { task_type: "poster", status: "running" };
      const f2: TaskControlFilter = { task_type: "poster", status: "running" };
      expect(filterHashMatches(filterToQueryString(f1), filterToQueryString(f2))).toBe(true);
    });

    it("different filters produce non-matching hashes", () => {
      const f1: TaskControlFilter = { task_type: "poster", status: "running" };
      const f2: TaskControlFilter = { task_type: "poster", status: "failed" };
      expect(filterHashMatches(filterToQueryString(f1), filterToQueryString(f2))).toBe(false);
    });

    it("order independence for same keys", () => {
      const qs1 = "task_type=poster&status=running";
      const qs2 = "status=running&task_type=poster";
      expect(filterHashMatches(qs1, qs2)).toBe(true);
    });

    it("handles empty query strings", () => {
      expect(filterHashMatches("", "")).toBe(true);
    });
  });

  describe("queryToFilter edge cases", () => {
    it("parses query string with numeric values as numbers", () => {
      const parsed = queryToFilter("task_type=poster&library_id=42&generation=3");
      expect(parsed.library_id).toBe(42);
      expect(parsed.generation).toBe(3);
    });

    it("ignores unrecognized keys silently", () => {
      const parsed = queryToFilter("foo=bar&task_type=scan&baz=123");
      expect(parsed.task_type).toBe("scan");
      expect(Object.keys(parsed).length).toBe(1);
    });

    it("handles empty search string", () => {
      expect(queryToFilter("?")).toEqual({});
      expect(queryToFilter("")).toEqual({});
    });
  });
});
