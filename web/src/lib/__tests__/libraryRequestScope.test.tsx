import { StrictMode, useEffect, useState } from "react";
import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ fetch: vi.fn() }));
vi.mock("../../api/client", () => ({ fetchLibrariesWithCapabilities: mocks.fetch }));
vi.mock("../../store/auth", () => ({ useAuthStore: (selector: (state: { token: string; username: string }) => unknown) => selector({ token: "token-a", username: "alice" }) }));

import { LibraryRequestScopeProvider, useLibraryRequestScope } from "../libraryRequestScope";

function Consumer() {
  const scope = useLibraryRequestScope();
  const [result, setResult] = useState("pending");
  useEffect(() => {
    const controller = new AbortController();
    void scope!.load(controller.signal).then(() => setResult("loaded"), () => setResult("failed"));
    return () => controller.abort();
  }, [scope]);
  return <div>{result}</div>;
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.fetch.mockImplementation(() => Promise.resolve({ items: [], drmCapabilities: {}, encryptedAssetsConfig: {} }));
});

describe("LibraryRequestScopeProvider", () => {
  it("survives StrictMode effect replay and shares one physical request", async () => {
    render(<StrictMode><LibraryRequestScopeProvider><Consumer /></LibraryRequestScopeProvider></StrictMode>);
    expect(await screen.findByText("loaded")).toBeInTheDocument();
    expect(mocks.fetch).toHaveBeenCalledTimes(1);
  });
});
