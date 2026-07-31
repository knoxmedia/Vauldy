# Large-file encryption optimization (resume + throughput)

Date: 2026-07-31  
Status: implemented on `feature/large-file-encrypt-optimization` (Phase 1+2 automated suites green 2026-07-31). Manual checklist still for PR: kill mid-encrypt ≥8 GiB resume; cross-vol no second plaintext on data disk. Community sync: paths under `internal/crypto`, `storage`, `postingest` are Vauldy-safe (no pretranscode).

## Goals

1. **Phase 1 (reliability):** Encrypt jobs for large media (especially ≥15 GiB / USB) can **resume after cancel, lease expiry, process restart, or transient I/O error** without re-reading/re-writing the entire ciphertext from offset 0.
2. **Phase 1 (layout):** Work correctly when ciphertext is **same-volume as source** *and* when ciphertext is on **app data disk** while source is on a slow volume (USB).
3. **Phase 2 (throughput):** Cut redundant full-file SHA256 / Sync / remux passes so wall-clock time for large encrypts is dominated by **one** read of plaintext + **one** write of ciphertext (plus unavoidable remux when required).

## Non-goals

- Changing the on-disk Knox 9527 envelope format (magic/version/CTR mode) or key wrap.
- Multi-threaded / multi-process parallel AES of one file (complexity vs USB-bound gain).
- Encrypting while the media is open for playback (plaintext-busy rules stay).
- Replacing Task Manager encrypt abort/reset/remove (already shipped).
- Guaranteeing progress across **source file content change** (size/mtime/path change invalidates the stage).

## Problem summary (current)

Linked ingest encrypt (`StageMediaEncryption` → commit state machine) for a large video typically pays:

| Cost | Cause |
|------|--------|
| Multiple full plaintext SHA256 | `EncryptionSourceFingerprint`, commit preflight `SourceFingerprint`, quarantine verify |
| Multiple full ciphertext SHA256 | `EncryptionPathHash` after stage + `hashPath` at commit |
| Full-file Sync | `dst.Sync()` after staged encrypt |
| Cross-volume copy | Quarantine root under `DataDir` while source on USB → `Rename` EXDEV → full copy |
| Optional later full cycle | Stage path skips faststart remux → `RepackEncryptedMP4ForPipe` decrypt+remux+re-encrypt |

AES-CTR itself (`ctrEncryptChunk = 256 KiB`, single-threaded) is usually **not** the limiter on USB.

Manual / unlinked `EncryptMedia*` is lighter (no multi-hash staging) but still remuxes video ISO and has **no resume**.

## Confirmed product decisions

| Item | Decision |
|------|----------|
| Priority | **C:** reliability first, then throughput |
| Ciphertext layout | **C:** must support same-volume *and* data-dir (cross-volume) deployments |
| Resume unit | Byte offset into plaintext / ciphertext (CTR counter-aligned); not “whole file only” |
| Identity | Prefer **cancellable** fingerprint; commit-time **rehash of entire source is not required** if path+size+mtime (or cached hash) still match |

## Architecture

```
Phase 1                          Phase 2
┌─────────────────────┐          ┌──────────────────────────┐
│ Resumable CTR stage │          │ Single-pass hash+encrypt │
│ Journal offsets     │   ──►    │ Larger buffers           │
│ Same-vol quarantine │          │ Deferred/policy Sync     │
│ No EXDEV full copy  │          │ Faststart before stage   │
│ Cancelable FP       │          │ Skip remux if moov-first │
└─────────────────────┘          └──────────────────────────┘
```

Shared invariants:

- Ciphertext remains AES-CTR with existing header; resume seeks source to `plain_offset` and continues CTR from `plain_offset / 16` block counter.
- Stage files live under existing stage root; journal row owns recovery.
- Commit still fences lease / generation / publication step (no weaker atomicity than today).

---

## Phase 1 — Reliability design

### 1.1 Resumable staged encrypt

**Extend** `media_encryption_stage_journal` (or equivalent stage metadata) with:

| Column / field | Meaning |
|----------------|---------|
| `plain_offset` | Next plaintext byte to read (0 = start) |
| `enc_bytes_written` | Ciphertext payload bytes after header (must equal `plain_offset` for CTR) |
| `state` | existing + `encrypting` (in progress) |

**Algorithm:**

1. If journal has `state=encrypting|staged` for this media/generation/task attempt **and** source identity still matches → open existing `.enc`, seek to `EncHeaderSize + enc_bytes_written`, seek plaintext to `plain_offset`, resume CTR.
2. Else create new stage file, write header, start at offset 0.
3. Every **N MiB** (default 64 MiB) or on cancel/error: flush writer, update journal offsets (`Sync` of journal DB row; optional periodic file `Sync` of stage — see Phase 2 policy).
4. On success: set `state=staged`, store final size/hash **once** (or defer hash to Phase 2 single-pass).

**Cancellation:** `EncryptFileContext` already stops between chunks; resume must not delete a partial stage on cancel when offsets were persisted (today delete-on-failure becomes “park for resume”).

**Invalidation:** If source path/size/mtime (or fingerprint) diverges → abandon stage, quarantine partial `.enc`, start fresh.

**Unlinked/manual path:** Either route through the same resumable stager, or add equivalent offset journal for `EncryptMediaManual`. Prefer **one** resumable implementation used by both linked and unlinked adapters.

### 1.2 Quarantine / layout policy (same-vol + cross-vol)

| Scenario | Policy |
|----------|--------|
| Source and enc base **same volume** | Quarantine root **on that volume** (prefer beside source / library `.quarantine`). Use `Rename` for plaintext move. |
| Source on USB, enc on **data disk** | Ciphertext may stay on data disk. Quarantine of plaintext: **same volume as source** only. **Never** `Copy` entire plaintext to data-disk quarantine on EXDEV. |
| EXDEV would be required to move plaintext to configured quarantine root | Fail closed with clear error **or** skip quarantine move when `cleanup_plaintext=0`; when cleanup is required, quarantine **locally next to source**, then delete after commit verify. |

Rationale: cross-volume full copies of 15 GiB+ are a primary failure mode and time sink.

### 1.3 Fingerprint / commit checks

- Replace full-file `EncryptionSourceFingerprint` in hot path with **context-aware** hashing (reuse `publication.SourceFingerprintContext` pattern) **or** a **quick identity** (abs path + size + mtime) for resume matching, with optional background/full hash stored once.
- At commit preflight: **do not** re-SHA256 entire plaintext if quick identity matches the staged fingerprint metadata.
- Quarantine verify: prefer size+mtime+path continuity; full SHA256 only when policy requires (e.g. EXDEV was avoided and cleanup is on — still prefer single cached hash from Stage time).

### 1.4 Recovery hooks

- Startup / `RecoverAllInterrupted`: leave resumable `encrypting` stages intact; requeue `post_ingest_task` to waiting so dispatcher resumes.
- Admin **abort**: soft cancel; after grace mark cancelled; **retain** partial stage until remove/reset explicitly discards it (or TTL GC).
- Admin **reset**: clear lease, keep stage if identity valid (resume), else discard stage.

### 1.5 Phase 1 success criteria

- Kill process at ~50% of a ≥8 GiB encrypt → restart → task completes **without** rewriting the first 50%.
- USB unplug mid-encrypt → failed/retryable or cancelled; after remount + reset/requeue → resumes from journal offset when file identity matches.
- Cross-volume config: encrypting 15 GiB source on USB to data enc dir **does not** create a second 15 GiB plaintext copy on data disk.

---

## Phase 2 — Throughput design

### 2.1 Single-pass hash

While encrypting, feed ciphertext (and optionally plaintext) into SHA256 via `io.MultiWriter` / Tee so `EncryptionPathHash` / commit `hashPath` become **O(1) metadata** reads, not second full-file passes.

### 2.2 IO tuning

- Raise CTR chunk (e.g. 1–4 MiB); use buffered readers/writers.
- **Sync policy:** default = sync journal offsets periodically; full `fd.Sync()` on stage complete and before commit, not after every small write. Optional “durable USB” profile can sync more often.

### 2.3 Faststart alignment

- `StageMediaEncryption` must call the same `resolveEncryptSource` / faststart gate as legacy `EncryptMedia` for video ISO.
- Use existing `isoBMFFMoovBeforeMDAT` (≤64 MiB scan): **skip** remux when already faststart.
- Goal: eliminate post-encrypt `RepackEncryptedMP4ForPipe` full decrypt+re-encrypt for newly staged files.

### 2.4 Phase 2 success criteria (same hardware baseline)

- Linked encrypt of a large non-remux-needed file: ≈ **1× read plain + 1× write enc** (+ small metadata), no second full enc SHA256 read.
- Remux-needed MP4: at most **one** remux pass before encrypt, not remux-after-encrypt.

---

## Testing

| Area | Tests |
|------|--------|
| CTR resume | Encrypt N bytes, interrupt, resume; decrypt equals full plaintext |
| Identity mismatch | Change mtime/size → old stage discarded |
| Cancel mid-stage | Offsets persisted; requeue resumes |
| Quarantine same-vol | Rename path used; no Copy |
| Quarantine cross-vol | No full plaintext copy to data quarantine |
| Single-pass hash (P2) | Hash matches independent `sha256` of enc file |
| Faststart skip (P2) | Moov-before-mdat → no ffmpeg remux |

Regression: existing encrypt fencing, lease, Task Manager abort/reset/remove.

## Rollout

1. Ship Phase 1 behind no flag if journal columns are additive and resume is backward compatible (old stages without offsets = start fresh).
2. Ship Phase 2 as follow-up PR once Phase 1 is green on USB + data-dir fixtures.
3. Sync community-safe slices to Vauldy after commercial validation (same dual-repo workflow).

## Open implementation notes (for plan, not blockers)

- Exact journal migration SQL and whether unlinked encrypt shares `media_encryption_stage_journal` vs a lighter `encrypt_progress` table.
- Default checkpoint interval (64 MiB suggested).
- Whether Task Manager should show encrypt **percent** from `plain_offset/size` (nice-to-have; not required for Phase 1).
