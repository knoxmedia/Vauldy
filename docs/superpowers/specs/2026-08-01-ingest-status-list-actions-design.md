# Ingest status list: scroll, reason, retry, remove

Date: 2026-08-01  
Status: approved (awaiting implementation)

## Goal

Improve Media Manager「入库状态」so operators can scan failures without a tall page, see why items failed/degraded, retry ingest, or delete the media after an explicit warning.

## Scope

**In:** `web/src/pages/MediaManager.tsx` ingest-status `Card` / `List`, i18n keys, existing client APIs.  
**Out:** New backend endpoints; changing publication/retry semantics; dismiss-without-delete.

## Behavior

1. **Scroll** — Card body `maxHeight: 320px`, `overflowY: auto`.「加载更多」stays below the scroll region (sibling under the list, not inside the scrolling body if that would hide it; prefer list scroll + footer button outside scroll).
2. **Reason** — For `failed` and `degraded` only, show `publication_error` under the path (secondary text). If empty, show a localized “no reason” placeholder.
3. **Retry** — Icon/text button only for `failed` / `degraded`. Calls `retryAdminMediaIngest(mediaId)`. On success: toast + refresh current library media page (same load path as today). On error: toast with message.
4. **Remove** — Same state scope as retry. `Popconfirm` (or equivalent) must warn that **the media and related data will be permanently deleted**. Confirm → `deleteMedia(mediaId)`. On success: remove row from local `rows` (and toast); on error: toast. Do not soft-dismiss.
5. **Other states** (`processing`, `cancelled`) — status Tag only; no reason emphasis, no retry/remove.

## Data

- List already comes from admin media (`IncludeUnpublished`), which returns `publication_error`.
- Ensure `AdminMediaItem` / row typing exposes `publication_error` if missing.
- No new API.

## UI sketch

```
[ Card: 入库状态 ]
  [ scrollable list max 320px ]
    title / path
    [reason for failed|degraded]
                              [Tag] [Retry?] [Remove?]
  [ Load more ]  // outside scroll
```

## i18n

Add keys under `pages.media_manager` (zh-CN / en / zh-TW as repo convention): reason empty, retry, remove, remove confirm title/body, retry/remove success/failure toasts.

## Tests

Extend `MediaManager.publication.test.tsx` (or sibling):

- Failed/degraded row shows error text (or placeholder).
- Retry calls `retryAdminMediaIngest`.
- Remove confirm then calls `deleteMedia`; cancel does not.
- Processing row has no retry/remove controls.

## Non-goals

- Matching AdminConsole queue-card height tricks.
- Per-step optional scrape retry from this list (full ingest retry only).
