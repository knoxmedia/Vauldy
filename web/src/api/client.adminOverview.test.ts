import { describe, expect, it, vi } from "vitest";
import { api, fetchAdminOverview } from "./client";

describe("fetchAdminOverview", () => {
  it("passes the caller signal and a 3000ms client timeout", async () => {
    const controller = new AbortController();
    const getSpy = vi.spyOn(api, "get").mockResolvedValue({ data: { monitor: {}, system: {}, activities: [] } });

    await fetchAdminOverview(controller.signal);

    expect(getSpy).toHaveBeenCalledWith("/api/v1/admin/overview", { signal: controller.signal, timeout: 3000 });
    getSpy.mockRestore();
  });
});
