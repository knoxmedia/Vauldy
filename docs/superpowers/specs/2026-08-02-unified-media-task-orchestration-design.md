# Unified Media Task Orchestration Design

Date: 2026-08-02
Status: Approved design
Scope: Knox Media commercial edition, with community-safe orchestration concepts where capabilities exist

## 1. Purpose

This design defines a phased evolution from multiple overlapping task systems into a unified orchestration layer that reuses existing executors. It covers scan, file discovery, upload, publication, asynchronous media processing, resource-aware scheduling, encrypted-source retry, and safe plaintext retirement.

The design intentionally does not replace mature poster, scrape, subtitle, preview, keyframe, audio-track, encryption, pretranscode, photo, lyric, document, or AI executors in one migration. The orchestrator owns planning and lifecycle; adapters continue to own media processing.

## 2. Current-state findings

The repository currently has three overlapping task models:

1. Publication-aware queues using `media_ingest_run`, `media_ingest_step`, dependencies, generation fencing, leases, evidence, and queue executions.
2. Domain/legacy task tables such as `subtitle_task`, `preview_task`, `atrack_task`, `keyframe_task`, `lyric_task`, photo tasks, transcode tasks, and package tasks.
3. Standalone scheduled or in-memory loops such as document-cover generation and periodic service loops.

Important gaps:

- `realtime_monitor` is periodic full-tree polling, not filesystem event notification.
- Uploads bypass Publication V2, become visible immediately, and ignore post-ingest enqueue errors.
- Publication planning omits automatic keyframe and audio-track steps despite existing adapters and configuration fields.
- Metadata probing runs synchronously inside scans and has no independent task lifecycle.
- Post-ingest concurrency only has explicit caps for global, poster, preview, and subtitle; other types share the global pool.
- Scrape, scan, transcode, pretranscode, lyric, photo, document, AI, and scheduled work use inconsistent state, priority, retry, lease, and concurrency semantics.
- `person_scrape_task` can be enqueued and listed but has no consumer.
- Document conversion is request-driven and document-cover work is in-memory.
- Current encryption depends only on poster/thumbnail; optional work normally starts after plaintext may already be removed.
- Package processing can directly delete its source without the publication DAG or durable cleanup recovery.
- Administrative encryption removal/reset can compromise journal recovery identity.

## 3. Approved product decisions

### 3.1 Entry behavior

- Manual and scheduled scans retain sequential, per-file synchronous ingest inside each library scan.
- Filesystem notifications and uploads create a durable ingest item and return immediately.
- Default ingest worker concurrency is 3.
- Different libraries may scan concurrently, subject to global scan and disk resource budgets.

### 3.2 Publication threshold

The synchronous publication chain is:

1. Parse/probe metadata.
2. Capture poster, thumbnail, artwork, or document cover where applicable.
3. Scrape metadata/artwork where applicable.
4. Build the immutable asynchronous task DAG, including retry nodes for any soft-failed synchronous work.
5. Atomically commit media visibility, the configuration snapshot, DAG nodes/edges, and queue execution rows in one transaction.

Metadata parsing is the only hard processing requirement for publication. Poster, thumbnail, artwork, cover, and scrape failure produce degraded-but-visible media plus a frozen retry node. A media row must never become visible before its plan commits.

### 3.3 Plaintext deletion threshold

Plaintext may be retired only when:

- ciphertext and key/evidence are committed and readable;
- every task in the immutable media plan is terminal;
- terminal means `done`, `skipped`, `failed`, or `cancelled`;
- all task types that can be retried after cleanup can consume encrypted media;
- source identity and generation still match the plan.

Failure is terminal and does not block cleanup after automatic retries are exhausted. Tasks still `waiting` or `running` block cleanup.

### 3.4 Planning snapshot

The task set is frozen when publication commits. Later library configuration changes affect new ingest generations only. Existing media requires an explicit replan/reprocess operation.

### 3.5 Priority

Default source priority is:

1. Manual operations and run-now.
2. Upload and automatic filesystem discovery.
3. Manual scans.
4. Scheduled scans and background repair/backfill.

Priority aging prevents starvation. Within equivalent priority, libraries rotate fairly before stable ordering by eligibility and age.

### 3.6 Delivery

Implementation is phased: safety and semantics first, then consistent entry paths, then resource scheduling, then the unified control plane, then remaining media types and legacy convergence.

## 4. Architecture boundaries

The system stores three separate notions of completion:

### 4.1 Publication completion

Controls frontend visibility. Metadata success is required; optional presentation/enrichment failures may still publish degraded media.

### 4.2 Media processing completion

All tasks from the immutable plan are terminal. This status is independent of publication visibility.

### 4.3 Plaintext retirement completion

Ciphertext is selected and verified, the processing barrier is satisfied, the original is quarantined/deleted, and deletion is verified. This status is independent of encryption task completion and publication.

These boundaries prevent the existing ambiguity where a terminal publication run is incorrectly treated as proof that all optional work is complete or that plaintext is safe to remove.

## 5. Unified orchestration model

### 5.1 Responsibilities

The orchestrator owns:

- immutable task plan/DAG creation;
- dependency evaluation and failure propagation;
- normalized lifecycle states;
- priority and aging;
- per-type concurrency admission;
- global resource token admission;
- leases, heartbeats, timeout, retry, and recovery policy;
- generation and retry-round fencing;
- encrypted-source capability requirements;
- unified task projection and audit events.

Executors own:

- tool invocation and media processing;
- artifact staging and validation;
- progress reporting;
- result/error classification;
- executor-specific cleanup of temporary artifacts.

### 5.2 Adapter contract

Each executor adapter declares:

- task type and supported media types;
- required capabilities;
- resource request (CPU, GPU, disk read/write, network, external process);
- timeout policy;
- retry classification;
- its encrypted-source strategy: `stream_decrypt`, `materialize_temp`, or `encrypted_derivative`;
- artifact/evidence output;
- cancellation behavior;
- terminal result mapping.

A task cannot be included in a cleanup-eligible plan unless its adapter declares one of the three encrypted-source strategies and passes that strategy's contract tests. There is no untyped retry-input escape hatch.

### 5.3 Durable task projection

A unified read model exposes, at minimum:

- queue family and task type;
- execution/domain row identity;
- media, library, ingest item, run, step, generation, and retry round;
- source origin;
- raw and normalized status;
- base and effective priority;
- `available_at`, created/started/finished timestamps;
- attempts and maximum attempts;
- worker owner, lease deadline, and heartbeat;
- dependency state;
- required resources and current admission blocker;
- last error and terminal reason;
- capabilities/actions allowed for that row.

Existing domain tables remain execution details during migration. Operator views read the unified projection rather than choosing one domain table as truth.

## 6. Library task options and dependency closure

Video library properties add these options:

- preview/sprite generation;
- subtitle extraction;
- audio-track extraction;
- subtitle recognition;
- keyframe extraction;
- AI analysis.

Dependency closure is:

```text
AI analysis
  -> subtitle recognition
       -> subtitle extraction
       -> audio-track extraction
```

Rules:

- Selecting subtitle recognition automatically selects subtitle extraction and audio-track extraction.
- Selecting AI analysis automatically selects subtitle recognition and therefore both extraction prerequisites.
- A selected dependency cannot be deselected while a dependent remains selected.
- The UI displays why an option is locked.
- Backend request validation computes the same closure; frontend behavior is not the integrity boundary.
- Configuration migration normalizes existing rows into a valid closure.
- The frozen plan records both explicit selections and dependency-added selections for diagnostics.

If subtitle recognition ends `failed` or `cancelled`, AI analysis is atomically marked `skipped` with a dependency-unsatisfied reason and is not executed. If subtitle recognition is later explicitly retried and succeeds, the administrator may explicitly reopen the existing AI node in the same generation with a new retry round and audit event. DAG topology, node identity, selection provenance, and edges remain immutable; only lifecycle fields change.

## 7. Entry flows

### 7.1 Manual and scheduled scan

```text
walk directory
  -> find new file
  -> synchronous ingest/publication chain
  -> freeze and persist async DAG
  -> continue to next file
```

One library scan processes new files sequentially. It must reuse the same planner and transaction semantics as ingest workers. Scan cancellation stops discovery and prevents new plans while safely fencing already committed plans.

### 7.2 Filesystem notification

Implement actual recursive filesystem events rather than periodic full scans:

- canonicalize the candidate path enough to identify its library and write a durable raw discovery event before acknowledgement;
- return from the event callback immediately after the durable inbox commit;
- asynchronously debounce bursty inbox events;
- wait for file size/mtime stability;
- fully canonicalize and validate the library path;
- calculate an idempotency key;
- create or merge a durable ingest item and mark the raw event consumed in one transaction.

Periodic scanning remains as reconciliation for missed events.

### 7.3 Upload

After upload bytes are durably finalized:

- create a durable ingest item;
- return HTTP 202 with ingest item identity;
- perform hash, probe, media insert, publication, and task planning in the ingest worker;
- never publish a row while silently dropping its task plan;
- assign uploads to a valid library/context or explicitly support upload-owned media in the planner.

Chunk upload endpoints may acknowledge chunk persistence separately. Merge completion acknowledges a queued ingest item rather than a published media row.

### 7.4 Ingest idempotency

All entry paths use a canonical source identity containing appropriate combinations of:

- library and normalized canonical path;
- upload identity;
- stable file fingerprint;
- size and mtime;
- media ID and expected generation once assigned.

Duplicate events, upload callbacks, scans, or retries converge on one current ingest item/generation. Changed source identity creates a replacement generation and fences stale work.

## 8. Media task templates

### 8.1 Video

Synchronous publication:

- metadata/probe;
- poster capture (soft failure);
- scrape (soft failure);
- publish and freeze plan.

Asynchronous tasks, controlled by library capabilities:

- preview/sprite;
- subtitle extraction;
- audio-track extraction;
- subtitle recognition;
- keyframe extraction;
- optimization/pretranscode;
- encryption;
- AI analysis subtasks.

### 8.2 Audio

Synchronous publication:

- metadata/probe;
- artwork extraction (soft failure);
- scrape (soft failure);
- publish and freeze plan.

Asynchronous tasks may include:

- lyric extraction/recognition;
- waveform/audio analysis;
- encryption;
- AI tags, classification, or summary.

### 8.3 Image

Synchronous publication:

- EXIF/metadata;
- thumbnail (soft failure under this design);
- scrape (soft failure);
- publish and freeze plan.

Asynchronous tasks may include:

- classification;
- geocoding;
- face detection/clustering;
- OCR and AI tags;
- encryption.

### 8.4 Document

Synchronous publication:

- metadata;
- cover (soft failure);
- publish and freeze plan.

Asynchronous tasks may include:

- durable document conversion/preview;
- full text extraction/OCR;
- AI summary/classification/tags;
- encryption where supported.

## 9. Dependency semantics

Dependencies are typed:

- `success`: predecessor must be `done` or an explicitly accepted success-equivalent state;
- `terminal`: predecessor may be any terminal state;
- `media_visible`: publication is visible;
- `all_plan_terminal`: every task in the frozen plan is terminal;
- `capability_policy`: plan creation has a registered adapter and an allowed encrypted-source strategy; runtime worker availability is an admission blocker, never a terminal dependency result;
- `resource`: scheduler admission only, not a durable DAG edge.

Subtitle-recognition-to-AI uses `success`. Plaintext retirement uses `all_plan_terminal` plus ciphertext and capability predicates.

Dependency propagation occurs transactionally when a task reaches a terminal state. Downstream tasks whose durable success dependency has become impossible become `skipped` with a structured reason. Temporary worker absence, deployment skew, resource shortage, or capability heartbeat loss leaves the task waiting and raises an operator-visible blocker; it never causes automatic skip.

## 10. Task lifecycle and retry

Normalized states are:

- `waiting`;
- `running`;
- `done`;
- `failed`;
- `cancelled`;
- `skipped`.

Only `waiting` and `running` are nonterminal.

Retry behavior:

- retryable error below limit: return to `waiting` with `available_at` and an incremented attempt;
- permanent error or exhausted retries: `failed`;
- shutdown/ownership uncertainty: retain/recover through lease fencing without inventing a terminal result;
- manual retry: increment monotonic `retry_round`; do not reset attempt identity to a previously used value;
- manual run-now: increase temporary effective priority/eligibility without claiming to bypass capacity.

Error records distinguish executor failure, dependency skip, cancellation, supersession, capability absence, timeout, uncertain commit, and cleanup follow-up failure.

## 11. Concurrency and resource scheduling

### 11.1 Default type limits

- ingest: 3;
- metadata: 3;
- manual scrape: 6;
- poster: 3;
- preview/sprite: 2;
- encryption: 1;
- audio-track extraction: 2;
- subtitle recognition: 1;
- keyframe extraction: 3;
- optimization/pretranscode: 1;
- document conversion: 2;
- AI analysis: 3.
- scan: 1 globally by default, while retaining one active scan lease per library.
- Any task type without an explicit configured limit inherits its adapter family limit; if neither exists, startup validation fails instead of treating it as unlimited.

The defaults are operator-configurable and are not necessarily the final throughput observed because global resource budgets also apply.

### 11.2 Resource budgets

Synchronous scan stages acquire the same metadata/poster/scrape type and resource tokens before executing, but remain in the scan goroutine and preserve per-library file order. The scheduler tracks configurable tokens for:

- CPU;
- GPU;
- disk reads;
- disk writes;
- network;
- external processes;
- optionally provider-specific concurrency/rate limits.

A task must obtain both its type token and all requested resource tokens before claim. Resources are reserved as part of claim/admission and released on completion, failure, cancellation, lease recovery, or worker loss.

Examples:

- metadata: CPU + disk read + external process;
- poster/preview/keyframe/atrack: CPU or GPU + disk read/write + ffmpeg process;
- ASR: GPU preferred or CPU-heavy fallback + disk read;
- encryption: CPU + disk read/write;
- scrape/remote AI: network + provider token.

CPU fallback after GPU initialization failure requires a fresh resource admission decision.

### 11.3 Priority, aging, and fairness

Effective ordering combines:

- source priority;
- explicit row priority/run-now boost;
- waiting-time aging that eventually crosses every source class;
- media-library fairness rotation;
- stable `available_at`, `created_at`, and ID ordering.

Aging must eventually cross every source-priority gap. Use `effective_priority = base_priority + floor(wait_age / aging_interval)`, with no cap below the highest base-priority gap; the initial design uses no aging cap. A run-now boost is a separate bounded, expiring row boost and does not rewrite task source. Library fairness prevents one large library from monopolizing a task type. Sustained-load tests must prove progress across every adjacent source class.

### 11.4 Runtime changes

`config.yml` provides defaults. Persistent database overrides can be changed through admin APIs/UI.

- Lowering a limit stops new claims but does not kill running work.
- A type can be paused, resumed, or drained.
- Invalid budgets/limits fail validation before activation.
- Configuration changes emit audit events.

## 12. Plaintext retirement

### 12.1 Separation from encryption

Encryption may produce and commit a ciphertext selection in parallel with other asynchronous tasks. Plaintext retirement is a separate durable task/state machine and cannot be inferred from encryption completion or publication completion.

### 12.2 Preconditions

Retirement requires:

- current, non-superseded generation;
- retirement is outside the frozen processing DAG and is excluded from `all_plan_terminal`;
- committed ciphertext selection;
- verified ciphertext hash/size and key reference;
- all frozen-plan tasks terminal;
- a tested encrypted-source strategy for every task that may later be retried;
- matching source path/fingerprint;
- no active source consumer lease;
- cleanup policy enabled.

### 12.3 States

- `blocked`;
- `ready`;
- `quarantining`;
- `quarantined`;
- `deleting`;
- `verified`;
- `retryable_failed`;
- `operator_required`.

The cleanup task has its own lease, retry round, attempt identity, error history, and audit record.

### 12.4 Filesystem transaction

1. Re-evaluate all preconditions under current generation/source identity.
2. Reserve a quarantine identity/path transactionally.
3. Move plaintext to quarantine.
4. Persist the quarantined state and evidence.
5. Delete quarantine content.
6. Verify absence and record `verified`.

A crash can either restore an uncommitted quarantine or continue a committed cleanup. Cleanup errors never route an already-completed encryption task through queue failure.

### 12.5 Existing risks to remove

- Replace encryption-journal `ON DELETE CASCADE` behavior with a recovery-safe policy or task tombstones.
- Reject administrative task removal while an active/recovery-required journal exists.
- Preserve monotonic attempt/retry identity across reset.
- Move package source deletion into the common retirement service; package workers must not call `os.Remove` directly.
- Replace bounded silent abandonment with durable exhausted/operator-required visibility.
- Resolve and enforce the meaning of `cleanup_plaintext` consistently.

## 13. Encrypted-source retry contract

The selected policy requires retries after plaintext cleanup to work from encrypted media.

Before a task type can be marked cleanup-compatible, tests must demonstrate that it can:

- use a decrypting ffmpeg source;
- materialize a bounded temporary plaintext file safely; or
- consume a task-specific encrypted derived asset.

Temporary plaintext must be placed in an approved protected directory, tied to task lease/generation, and cleaned on success, failure, cancellation, and startup recovery.

Tasks that cannot meet this contract keep plaintext retirement blocked until implementation is upgraded or the operator explicitly changes the plan/policy.

## 14. Task Manager control plane

### 14.1 Page ownership

All task data, task resource budgets, task health, and task operations move from **Administration → Console** to **Task Manager**. The Console retains only non-task system information:

- CPU, memory, and disk health;
- SQLite health/contention;
- service process health;
- system logs and other host-level status.

Worker availability shown in Console is host/service health only. Task-capability, worker-to-task admission, queue saturation, and task resource budgets belong to Task Manager. Console must not keep a second task count or task-action surface.

### 14.2 Tabs and navigation

Task Manager uses a horizontally scrollable tab bar. Every entry below is an independent task list tab; group labels are non-selectable separators:

- **Overview** (always first and not a task list);
- **Ingest**: Pending ingest; Scan; Metadata probe;
- **Publication/base processing**: Poster; Image thumbnail; Artwork/cover; Scrape;
- **Video post-processing**: Preview/sprite; Subtitle extraction; Audio-track extraction; Subtitle recognition; Keyframe; Batch transcode; Optimization/pretranscode; Package/DRM; Encryption;
- **Audio processing**: Lyric extraction/recognition; Waveform/audio analysis;
- **Image processing**: Photo classification; Photo geocoding; Face detection/clustering; Image OCR;
- **Intelligence/document**: AI analysis (with capability-subtask filter); Person scrape; Document conversion; Document full-text/OCR;
- **Maintenance**: Plaintext cleanup; Derived-file cleanup; Scheduled tasks.

A task type that is not implemented yet still has a registry descriptor and an explicit unavailable-capability state once its plan/schema is introduced; it must not silently disappear. New task types register a tab descriptor (task type, label, family, columns, filters, actions, route) rather than adding another hard-coded page branch. Combined labels such as preview/sprite represent one declared task type; distinct execution types such as poster and thumbnail, or transcode and optimization, never share a list identity.

### 14.3 Overview tab

Overview is a drill-down dashboard, not a competing task table. It shows:

- all-task counts by normalized status;
- counts by task type and status;
- current running tasks;
- oldest waiting tasks and queue age;
- failed, cancelled, blocked, and no-capable-worker summaries;
- per-type concurrency and global resource budgets;
- task worker/capability health;
- expired leases and recovery work;
- plaintext-cleanup exceptions;
- recent batch operations.

Every type-specific count or exception opens its independent task tab with identical server-side filters. Cross-type aggregates (for example all failed, all running, or oldest waiting) open a paginated unified-result drawer inside Overview, backed by the same projection/filter API; selecting a row then opens its concrete type tab and preserves filters. Overview does not become a second editable task list.

### 14.4 Single state/count source

Overview, tabs, filters, SSE/poll snapshots, and operations all read the same unified orchestration projection. Domain tables are executor details and may not independently override list status or dashboard counts.

A lifecycle mutation commits these effects atomically where applicable:

1. execution row/lease or cancellation request;
2. logical plan node status and retry round;
3. dependency propagation and downstream skip/reopen eligibility;
4. media-plan completion projection;
5. plaintext-retirement barrier recomputation;
6. operation audit event.

The mutation response contains the committed unified row/revision. The client may render a pending action indicator, but must not invent a terminal optimistic status.

### 14.5 Lists and explainability

Every task tab supports server-side cursor pagination, totals, and explicit truncation indicators. Common filters include normalized/raw status, source, library, generation, capability, owner, blocker, removed state, and time range.

For each waiting task, expose why it is not runnable:

- dependency not met;
- dependency permanently unsatisfied;
- backoff until `available_at`;
- task type paused;
- type concurrency exhausted;
- resource budget exhausted;
- no capable worker;
- current generation mismatch/superseded;
- source/ciphertext capability barrier.

Display actual effective claim order rather than endpoint-specific newest-first order.

### 14.6 Allowed actions contract

The backend returns state- and capability-checked actions for every row:

```json
{
  "allowed_actions": {
    "abort": true,
    "remove": false,
    "reset": false,
    "run_now": false,
    "skip": false
  }
}
```

The UI only renders allowed actions, and the mutation endpoint revalidates current status, generation, retry round, owner fencing, journal safety, and dependencies. A stale action receives a conflict response containing the latest unified row.

### 14.7 Abort

Abort is available only for running work:

1. persist an abort request;
2. signal the owning worker;
3. worker cooperatively stops, cleans temporary resources, and acknowledges;
4. commit `cancelled` as the terminal task result;
5. propagate dependencies and recompute plan/cleanup state.

Until acknowledgement, the row displays `running` with `abort_requested_at`; it must not pretend to be cancelled. If the worker does not acknowledge before the stop deadline, normalized status remains nonterminal `running`; `execution_condition=abort_timeout_recovery_required` identifies the blocker. It continues to block dependencies and plaintext retirement. After lease expiry and proof that the old owner cannot commit, recovery releases reservations exactly once and transactionally commits `cancelled` because abort intent remains authoritative. If ownership cannot be fenced safely, it remains nonterminal and requires operator reconciliation.

### 14.8 Remove

Remove is a logical tombstone, never an immediate physical deletion:

- request cancellation when the execution is cancellable;
- set `removed_at`, `removed_by`, and `remove_reason`;
- hide the row from default lists while allowing an explicit “show removed” filter;
- retain attempts, dependencies, audit history, encryption journals, cleanup evidence, and recovery authority.

Logical tombstoning is permitted while a recovery journal exists because it does not delete, cancel, or detach recovery authority; recovery continues against the hidden tombstoned identity. Physical deletion/purge is rejected while any active execution, dependency, journal, cleanup state, or audit retention rule references the row. The earlier prohibition on administrative removal during journal recovery refers to physical deletion, not this logical tombstone. Removal must not delete source media or generated artifacts unless a separate explicit cleanup operation owns that behavior.

### 14.9 Reset

Reset creates a new monotonic `retry_round`; it never zeroes or reuses historical attempt identity:

- preserve previous attempts, errors, timings, and owner history;
- validate current generation, source/ciphertext strategy, dependencies, and task policy;
- create/reopen the next waiting execution round without mutating DAG topology;
- clear only obsolete current-lease fields;
- write operator, reason, previous state, and new retry round to the audit log.

A reset re-evaluates downstream nodes. For subtitle recognition, later successful completion may make an explicitly selected, previously dependency-skipped AI node eligible for an explicit reopen. AI does not reopen merely because reset was requested or because recognition is still waiting/running.

### 14.10 Batch operations

Batch operations use the same row semantics and return:

- `operation_id`;
- per-item success/failure;
- committed unified status and revision;
- structured failure reason;
- a retryable failed subset;
- complete audit metadata.

Each item is an independent transaction; the batch is not all-or-nothing. `operation_id` is a required idempotency key, and `(operation_id,item_id,action)` has one durable outcome. Replaying after a transport failure returns prior committed outcomes and cannot create duplicate retry rounds or repeat mutations. Destructive or broad operations require confirmation proportional to impact. Batch success counters must be derived from per-item committed results, not from requests accepted for later processing.

## 15. Observability

Expose per type/family:

- waiting/running/terminal counts;
- queue age p50/p95/max;
- throughput;
- failure, retry, cancellation, and skip rates;
- type concurrency used/limit;
- resource tokens used/limit;
- saturation duration;
- lease expiry/recovery counts;
- worker heartbeat, capabilities, version, and last error;
- publication blockers and dependency propagation;
- cleanup blocked/retryable/operator-required counts;
- journal recovery state;
- SQLite contention metrics with their process-local limitation clearly labeled.

Task Manager Overview must include subtitle and burst/resource usage currently omitted by the client and must surface publication/recovery diagnostics rather than hiding them. Administration → Console must remove task counters/actions after parity is verified.

## 16. Configuration

Illustrative defaults:

```yaml
scheduler:
  concurrency:
    ingest: 3
    metadata: 3
    scrape: 6
    poster: 3
    preview: 2
    encrypt: 1
    atrack: 2
    subtitle_recognize: 1
    keyframe: 3
    prepare: 1
    document_convert: 2
    ai: 3
  resources:
    cpu: 4
    gpu: 1
    disk_read: 3
    disk_write: 2
    network: 6
    external_process: 4
  priority:
    manual: 400
    upload_or_discovery: 300
    manual_scan: 200
    scheduled_or_repair: 100
    aging_interval: 5m
    aging_step: 1
    # no aging cap: every source class must eventually make progress
```

Configuration precedence is: validated persistent database override > validated `config.yml` value > compiled default. Overrides survive restart and record schema version, revision, author, and audit timestamp. Updates validate and activate atomically; invalid updates leave the prior revision active. Zero means paused for a task-type concurrency limit and disabled/unavailable for a resource capacity, and the UI must label the resulting blocker explicitly.

## 17. Migration phases

### Phase 1: semantics and plaintext safety

- Add library processing options and dependency closure.
- Extend immutable planning to keyframe, atrack, subtitle recognition, and AI subtasks.
- Add dependency kinds and transactional skip propagation.
- Add media-plan completion projection.
- Introduce plaintext-retirement state machine and worker.
- Stop direct package source deletion.
- Fix encryption journal deletion/reset identity risks.
- Add encrypted-source capability declarations and compatibility tests.

### Phase 2: consistent ingest entry points

- Add durable `ingest_item` and default concurrency 3.
- Move upload post-save processing into ingest items and return 202/task identity.
- Implement true filesystem event discovery with reconciliation scans.
- Reuse one planner across scanner and ingest workers.
- Enforce idempotency and generation fencing across all sources.

### Phase 3: resource-aware scheduler

- Add per-type concurrency limits.
- Add global resource-token accounting.
- Add source priority, aging, and library fairness.
- Adapt existing executors incrementally.
- Add config defaults and persistent runtime overrides.
- Add pause/resume/drain.

### Phase 4: unified operator control plane

- Move task summaries, task budgets, and task operations from Administration → Console to Task Manager; leave system-only health in Console.
- Add Overview as the first Task Manager tab and independent horizontally scrollable tabs for every task type.
- Build the unified task projection and cursor APIs as the single source for overview and lists.
- Show actual claim order and blocker explanations.
- Add task worker/resource health and queue metrics.
- Implement capability-driven abort, tombstone remove, monotonic retry-round reset, and audited batch operations.
- Surface publication and cleanup recovery diagnostics.

### Phase 5: other media and legacy convergence

- Add audio, image, and document task templates.
- Make document conversion persistent.
- Split AI work into capability subtasks.
- Implement person scrape consumption.
- Remove dead legacy loops and direct work-spawning goroutines.
- Normalize domain status vocabularies and delete redundant projections only after migration.

## 18. Testing strategy

### 18.1 Planning and dependency tests

- Every media-type/library-option combination freezes the expected DAG.
- Subtitle recognition closure selects subtitle and audio-track extraction.
- AI closure selects subtitle recognition and both extraction prerequisites.
- Illegal option combinations are rejected/normalized server-side.
- Recognition final failure atomically skips AI.
- Recognition retry success can explicitly reopen AI with a new retry round.

### 18.1a Frozen-DAG integrity tests

- DAG is acyclic and all typed edges reference known nodes.
- Plan schema/configuration version is persisted and unknown task/dependency types are rejected.
- Topology, node identity, provenance, and edges cannot mutate after commit.
- Visibility, plan, and queue execution rows roll back atomically on any planning failure.

### 18.2 Entry/idempotency tests

- Duplicate scan/event/upload submissions converge on one ingest item/generation.
- Upload acknowledgement never implies publication.
- Crash after upload/event acknowledgement but before worker claim cannot lose the durable ingest item/raw event.
- A scan finishes each file's synchronous publication transaction before starting the next file and does not route scan files through the asynchronous ingest queue.
- Poster/artwork/cover/scrape soft failure still commits degraded visibility and its frozen retry node.
- Planning failure cannot leave a visible unplanned media row.
- Source change creates a replacement generation and fences stale workers.

### 18.3 Scheduler tests

- Per-type and resource limits are both enforced.
- Limit lowering does not kill current work.
- Aging prevents starvation.
- Library fairness prevents monopoly.
- Run-now does not bypass unavailable resources.
- GPU-to-CPU fallback reacquires correct resources.
- Lease loss returns all reservations exactly once.

### 18.4 Lifecycle tests

- Retryable error below limit remains nonterminal waiting.
- Permanent failure is immediately terminal.
- Exhausted retry becomes terminal failed.
- Cancelled and skipped are terminal.
- Cancelled subtitle recognition skips AI and AI does not reopen automatically.
- Dependency-impossible descendants become skipped, not permanently waiting.
- Generation replacement fences completion and cleanup.

### 18.5 Plaintext-retirement race tests

- Encryption commits while optional tasks remain waiting/running: no deletion.
- First-attempt permanent optional failure: barrier advances.
- Retryable optional failure below limit: barrier remains blocked.
- Logical remove with a staged/quarantined journal creates a tombstone while preserving recovery; physical purge is rejected.
- Admin retry never reuses execution identity.
- Crash before/after quarantine move/commit/delete is recoverable.
- Package success cannot directly remove source.
- Cleanup failure after encryption success does not fail encryption.
- Cleanup retries after restart and exposes operator-required exhaustion.
- Every cleanup-compatible executor succeeds against encrypted input.
- Materialized temporary plaintext is protected, size/time bounded, lease-bound, and recovered after success, failure, cancellation, worker loss, and restart.

### 18.6 Control-plane tests

- Server pagination and totals are correct.
- Display order matches effective claim order.
- Blocker explanation corresponds to the scheduler decision.
- Batch operation reports every item and is auditable.
- Hidden/truncated data is explicitly indicated.
- Persistent admin overrides survive restart and retain precedence over changed `config.yml` defaults.
- Console exposes no task counts or actions after migration; Task Manager Overview is the sole task dashboard.
- Type-specific Overview drill-down opens the correct independent type tab with identical filters and totals.
- Cross-type Overview drill-down opens the unified-result drawer; selecting a row opens its independent type tab.
- Every registered current/planned task type has exactly one independent list-tab identity and grouped horizontal navigation does not merge distinct types.
- Overview, type list, mutation response, and SSE snapshot expose the same unified revision and normalized status.
- Abort remains running/abort-requested until worker acknowledgement, then commits cancelled and releases reservations exactly once.
- Abort timeout remains nonterminal and blocks dependencies/retirement until fenced lease recovery commits cancelled or operator reconciliation completes.
- Remove creates a tombstone and preserves active/recovery journal authority; unsafe physical purge is rejected.
- Reset creates a monotonic retry round without reusing attempts or changing DAG topology.
- Recognition reset/success leaves AI skipped until a separate authorized AI reopen creates its own retry round and audit event.
- Stale allowed action returns conflict plus the latest unified row.
- Batch operation counters equal committed per-item results and failed subsets can be retried.
- Replaying the same operation ID after an ambiguous response returns prior per-item outcomes without duplicate reset rounds or mutations.

## 19. Acceptance criteria

The design is complete when:

- scan, file events, and uploads cannot create visible media without an immutable task plan;
- scans remain sequential per library while file events/uploads return after durable ingest enqueue;
- library processing options and dependency closure behave identically in UI and backend;
- subtitle-recognition failure skips AI analysis;
- task defaults and global resources constrain real concurrency;
- priority aging and library fairness are observable and tested;
- publication, plan completion, encryption completion, and plaintext retirement are separate durable outcomes;
- source deletion occurs only after the frozen-plan terminal barrier and encrypted-source compatibility checks;
- no worker directly deletes a media source outside the retirement service;
- Task Manager Overview is the sole task dashboard; Administration → Console is system-only;
- all current and planned task types have independent grouped, horizontally scrollable tabs and cross/type drill-down uses the same unified projection;
- overview counts, lists, mutation responses, and streams share one revision/status source;
- server-authoritative allowed actions make abort, tombstone remove, monotonic reset, explicit AI reopen, and idempotent per-item batch outcomes effective and auditable;
- phased migration preserves generation fencing, leases, evidence, and crash recovery.

## 20. Non-goals

- Rewriting all media executors in one phase.
- Introducing a distributed cluster scheduler before the local durable model is consistent.
- Making AI analysis one monolithic opaque task.
- Treating publication completion as proof of optional processing or plaintext cleanup.
- Guaranteeing immediate execution for run-now when resources are unavailable.









