# Local Widevine License Flow Verification

## Scope

This checklist verifies Knox local Widevine license issuance behavior after removing proxy-dependent flow.

## Automated Verification

- Backend suite (run in `media`):
  - `go test ./api/... ./internal/drm/... ./internal/config/... -count=1`
- Frontend suite (run in `media/web`):
  - `npm run test`
- Frontend production build (run in `media/web`):
  - `npm run build`

## Local Widevine Verification Checklist

- License endpoint enforces auth (missing/invalid token is rejected).
- License endpoint validates media and DRM asset binding before token issuance.
- JSON compatibility mode returns structured local response for Widevine challenge flow.
- Raw compatibility mode (`raw=1` or `Accept: application/octet-stream`) returns octet-stream payload.
- No upstream third-party proxy request path is exercised during license issuance.
- Local signing/audit path is hit for successful requests.

## Manual Smoke Notes

- Start backend and web player in local environment.
- Open DRM playback page for a media item with a valid Widevine DRM asset.
- Confirm playback starts without `6008` caused by request/response adaptation mismatch.
- Inspect network for `/api/v1/drm/widevine/license` and confirm responses come from local backend path.
- Confirm unauthenticated request returns `401`.
- Confirm empty challenge request returns `400` with `challenge is required`.
- Check backend logs/audit table for local issuance traces (`allowed`) and reject traces (`empty_challenge`), and no proxy-forward pattern.