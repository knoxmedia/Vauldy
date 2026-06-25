import { describe, expect, it } from "vitest";
import {
  adaptWidevineLicenseRequest,
  adaptWidevineLicenseResponse,
  isShakaInvalidWidevineServerCertificate,
  isShakaLoadInterrupted,
} from "../Player";

describe("player widevine drm adaptation", () => {
  it("adapts based on widevine license URL and body only", () => {
    const requestWithLegacyFlagTrue = {
      uris: ["/api/v1/drm/widevine/license"],
      headers: {} as Record<string, string>,
      method: "POST",
      body: new Uint8Array([1, 2, 3]),
      widevine_raw_proxy: true,
    } as any;
    const requestWithLegacyFlagFalse = {
      uris: ["/api/v1/drm/widevine/license"],
      headers: {} as Record<string, string>,
      method: "POST",
      body: new Uint8Array([1, 2, 3]),
      widevine_raw_proxy: false,
    } as any;

    const changedWhenTrue = adaptWidevineLicenseRequest(requestWithLegacyFlagTrue, 77);
    const changedWhenFalse = adaptWidevineLicenseRequest(requestWithLegacyFlagFalse, 77);

    expect(changedWhenTrue).toBe(true);
    expect(changedWhenFalse).toBe(true);
    expect(new TextDecoder().decode(requestWithLegacyFlagTrue.body)).toBe(
      JSON.stringify({ media_id: 77, challenge: "AQID" }),
    );
    expect(new TextDecoder().decode(requestWithLegacyFlagFalse.body)).toBe(
      JSON.stringify({ media_id: 77, challenge: "AQID" }),
    );
  });

  it("always adapts /drm/widevine/license request to local json payload", () => {
    const rawChallenge = new Uint8Array([1, 2, 3, 4]);
    const request = {
      uris: ["/api/v1/drm/widevine/license"],
      headers: {} as Record<string, string>,
      method: "POST",
      body: rawChallenge,
    };

    const changed = adaptWidevineLicenseRequest(request, 99);

    expect(changed).toBe(true);
    expect(request.headers["Content-Type"]).toBe("application/json");
    expect(request.method).toBe("POST");
    expect(new TextDecoder().decode(request.body)).toBe(
      JSON.stringify({ media_id: 99, challenge: "AQIDBA==" }),
    );
  });

  it("does not adapt non-widevine URL even when legacy flag-like field is present", () => {
    const request = {
      uris: ["/api/v1/drm/playready/license"],
      headers: {} as Record<string, string>,
      method: "POST",
      body: new Uint8Array([9, 8, 7]),
      widevine_raw_proxy: true,
    } as any;

    const changed = adaptWidevineLicenseRequest(request, 11);

    expect(changed).toBe(false);
    expect(request.headers["Content-Type"]).toBeUndefined();
    expect(new Uint8Array(request.body)).toEqual(new Uint8Array([9, 8, 7]));
  });

  it("decodes local wrapped widevine response payload", () => {
    const wrapped = new TextEncoder().encode(
      JSON.stringify({ license: "AQID" }),
    );

    const out = adaptWidevineLicenseResponse(wrapped);

    expect(Array.from(out)).toEqual([1, 2, 3]);
  });

  it("detects Shaka INVALID_SERVER_CERTIFICATE (DRM 6004)", () => {
    expect(isShakaInvalidWidevineServerCertificate({ category: 6, code: 6004 })).toBe(true);
    expect(isShakaInvalidWidevineServerCertificate({ category: 6, code: 6003 })).toBe(false);
    expect(isShakaInvalidWidevineServerCertificate(new Error("x"))).toBe(false);
  });

  it("detects Shaka LOAD_INTERRUPTED (PLAYER 7000)", () => {
    expect(isShakaLoadInterrupted({ category: 7, code: 7000 })).toBe(true);
    expect(isShakaLoadInterrupted({ category: 7, code: 7001 })).toBe(false);
  });
});
