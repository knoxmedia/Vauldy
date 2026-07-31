# Post-ingest priority scheduling

## Goal

1. Claim order: `poster`/`poster_repair` > `encrypt` > `preview`=`thumbnail`=`keyframe`=`atrack` > `subtitle`.
2. When global slots are full of lower-priority work, allow **one** additional high-priority task (`poster`/`poster_repair`/`encrypt`) without preempting runners (oversubscribe `Global+1`).

## Behavior

- Normal path: claim while `globalUsed < Global`, types ordered by priority; equal band rotates.
- Burst path: if `globalUsed == Global`, no high-priority task is running, and a high-priority task is eligible, allow claim/launch so `globalUsed` may reach `Global+1`.
- Poster/preview sub-caps unchanged.
- No preemption; burst slot frees when the high-priority task finishes.

## Implementation

- Dispatcher-only: priority-ordered `allowedTaskTypes`, global channel capacity `Global+1`, burst gated in allow/acquire logic.
