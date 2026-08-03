# Unified Media Task Orchestration Roadmap

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement each phase task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the approved unified media-task orchestration design through independently testable phases while preserving Publication V2 fencing and crash recovery.

**Architecture:** Keep the immutable publication plan as the orchestration authority and reuse existing task executors through adapters. Separate publication, all-plan completion, encryption selection, and plaintext retirement; introduce consistent entry paths, resource-aware admission, and the Task Manager control plane in later phases.

**Tech Stack:** Go 1.22, `database/sql`, modernc SQLite, Gin, React 19, TypeScript 6, Ant Design, Vitest/Testing Library, PowerShell.

**Specification:** `docs/superpowers/specs/2026-08-02-unified-media-task-orchestration-design.md`

---

## Phase plans

### Phase 1: Task semantics and plaintext safety

Detailed plan: `docs/superpowers/plans/2026-08-02-unified-media-task-orchestration-phase-1.md`

Delivers:

- library processing options and dependency closure;
- expanded immutable DAG and success dependency semantics;
- transactional skip propagation and all-plan completion projection;
- encrypted-source strategy contracts;
- monotonic encryption retry identity and journal-safe tombstones;
- durable plaintext-retirement state machine;
- removal of direct package source deletion and legacy in-memory source cleanup.

Exit gate: no code outside the retirement service can delete an authoritative media source, and cleanup remains blocked until every frozen plan node is terminal and every retryable executor has a tested encrypted-source strategy.

### Phase 2: Consistent ingest entry paths

Planned document: `docs/superpowers/plans/2026-08-02-unified-media-task-orchestration-phase-2.md`

Delivers:

- durable raw filesystem-event inbox;
- durable `ingest_item` queue with default concurrency 3;
- upload `202 Accepted` flow after durable enqueue;
- shared planner for scan, event, and upload sources;
- path/fingerprint idempotency and generation replacement;
- true filesystem events with periodic reconciliation scans.

Exit gate: scan, event, and upload sources cannot publish visible media without an atomically committed immutable plan; acknowledged event/upload work survives process failure before worker claim.

### Phase 3: Resource-aware scheduler

Planned document: `docs/superpowers/plans/2026-08-02-unified-media-task-orchestration-phase-3.md`

Delivers:

- per-task-type concurrency limits;
- CPU/GPU/disk/network/external-process tokens;
- source priority, unbounded progress aging, and library fairness;
- runtime override revision model;
- pause, resume, and drain;
- task admission explanations.

Exit gate: real execution respects both type and resource limits, all source classes make progress under sustained load, and lease loss releases each reservation exactly once.

### Phase 4: Task Manager control plane

Planned document: `docs/superpowers/plans/2026-08-02-unified-media-task-orchestration-phase-4.md`

Delivers:

- Task Manager Overview as the sole task dashboard;
- independent grouped horizontal tabs for every task type;
- unified projection, revisions, cursor pagination, and drill-down;
- server-authoritative `allowed_actions`;
- cooperative abort, tombstone remove, monotonic reset, explicit AI reopen;
- idempotent per-item batch operations;
- Console reduced to system-only health.

Exit gate: Overview counts, type lists, mutation responses, and streams expose one normalized status/revision source; all actions change durable execution state and are audited.

### Phase 5: Other media and legacy convergence

Planned document: `docs/superpowers/plans/2026-08-02-unified-media-task-orchestration-phase-5.md`

Delivers:

- audio, image, and document DAG templates;
- persistent document conversion and OCR/full-text tasks;
- capability-specific AI subtasks;
- person-scrape consumption;
- removal of dead loops and direct work-spawning goroutines;
- normalized domain states and retirement of redundant projections.

Exit gate: all registered task types have durable execution, unified lifecycle projection, encrypted-source policy where required, and Task Manager coverage.

## Cross-phase constraints

- Never mutate frozen DAG topology within a generation.
- Never treat publication status as all-plan completion.
- Never treat encryption completion as plaintext-retirement completion.
- Never skip work because a worker is temporarily unavailable.
- Never reset a historical attempt identity.
- Never physically delete task recovery authority through ordinary admin remove.
- Each phase must preserve existing generations, leases, evidence, and startup recovery before the next phase begins.
