# Task priority + batch operations

Date: 2026-08-01  
Status: implementing

## Rules

1. Queue tables get `priority INTEGER NOT NULL DEFAULT 0`. Higher runs first.
2. Claim order: `priority DESC, available_at, created_at, id` (type bands in dispatcher unchanged).
3. Run-now: bump `priority` to `MAX(priority)+1` and set `available_at` far in the past so the row jumps the FIFO among the same task type.
4. TaskManager: row selection + batch actions (retry / delete / stop / run-now) per tab where APIs exist.

## Tables

- `post_ingest_task` (subtitle, encrypt, preview, keyframe, atrack, poster…)
- `scrape_task`, `transcode_task` (prepare family)
- `lyric_task` (worker ORDER BY priority)

## Batch API shape

`POST /api/v1/{domain}/task/batch` body `{ "action": "retry"|"delete"|"cancel"|"run_now", "ids": [...] }`  
Subtitle/lyric/preview use `media_ids`; encrypt/transcode use queue `ids`.
