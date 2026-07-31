import { describe, expect, it } from "vitest";
import { matchesDisplayStatus, toDisplayStatus } from "./taskDisplayStatus";

describe("toDisplayStatus", () => {
  it("maps subtitle pending to waiting and preview ready to done", () => {
    expect(toDisplayStatus("subtitle", "pending")).toBe("waiting");
    expect(toDisplayStatus("subtitle", "running")).toBe("running");
    expect(toDisplayStatus("preview", "ready")).toBe("done");
    expect(toDisplayStatus("preview", "waiting")).toBe("waiting");
    expect(toDisplayStatus("atrack", "waiting")).toBe("waiting");
  });

  it("filters by display vocabulary", () => {
    expect(matchesDisplayStatus("subtitle", "pending", "waiting")).toBe(true);
    expect(matchesDisplayStatus("subtitle", "pending", "pending")).toBe(false);
    expect(matchesDisplayStatus("preview", "ready", "done")).toBe(true);
    expect(matchesDisplayStatus("preview", "ready", "ready")).toBe(false);
    expect(matchesDisplayStatus("subtitle", "failed", "all")).toBe(true);
  });
});
