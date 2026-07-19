import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ patch: vi.fn() }));
vi.mock("axios", () => ({ default: { create: () => ({
  patch: mocks.patch,
  interceptors: { request: { use: vi.fn() }, response: { use: vi.fn() } },
}) } }));
vi.mock("antd", () => ({ message: { error: vi.fn() } }));

import { batchUpdateDocumentTags } from "./client";

describe("batchUpdateDocumentTags", () => {
  beforeEach(() => mocks.patch.mockReset());
  it("sends exactly one PATCH with ids, mode, tags, and signal", async () => {
    const controller = new AbortController();
    mocks.patch.mockResolvedValue({ data: { updated: 2, items: [{ media_id: 2, tags: ["A"] }, { media_id: 1, tags: ["A"] }] } });
    const result = await batchUpdateDocumentTags([2, 1], "replace", ["A"], controller.signal);
    expect(mocks.patch).toHaveBeenCalledTimes(1);
    expect(mocks.patch).toHaveBeenCalledWith("/api/v1/documents/tags", { media_ids: [2, 1], mode: "replace", tags: ["A"] }, { signal: controller.signal });
    expect(result.updated).toBe(2);
  });
});
