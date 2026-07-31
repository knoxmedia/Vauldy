# Large-file encrypt optimization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make ≥15 GiB encrypt jobs resumable and cut redundant full-file hash/Sync/remux so USB and data-dir layouts both finish reliably and faster.

**Architecture:** Keep Knox 9527 AES-CTR. Add a dedicated `media_encrypt_resume` table (avoid mutating the strict `media_encryption_stage_journal` CHECK). Phase 1: resumable CTR + same-volume quarantine + cancelable/quick identity. Phase 2: single-pass hash, larger buffers, faststart-before-stage. One stager shared by linked and unlinked encrypt.

**Tech Stack:** Go, SQLite, existing `internal/crypto` CTR, `internal/storage` AssetEncryptor, `internal/postingest` encrypt adapter/quarantine.

**Spec:** `docs/superpowers/specs/2026-07-31-large-file-encrypt-optimization-design.md`

---

## File map

| File | Responsibility |
|------|----------------|
| `internal/crypto/ctr.go`, `envelope.go` | Resumable CTR encrypt from offset; larger chunk; optional hash tee |
| `internal/crypto/*_test.go` | Resume + cancel + hash tee tests |
| `internal/store/db.go` (or small migration helper) | `media_encrypt_resume` DDL |
| `internal/storage/encrypt_resume.go` (new) | Resume row CRUD + quick identity |
| `internal/storage/encrypt_volume.go` (new) | Same-volume root selection helpers |
| `internal/storage/staged_encrypt.go` | Resumable stage encrypt; no delete-on-cancel of progress |
| `internal/storage/asset_encrypt.go` | Manual path uses shared stager / resume |
| `internal/storage/enc_mp4_prepare.go` | Reuse from stage path; moov-first skip |
| `internal/postingest/encryption_quarantine.go` | Refuse EXDEV full plaintext copy to foreign volume |
| `internal/postingest/adapters.go` + `encryption_state_machine.go` | Commit without re-full-hash when identity matches; resolve quarantine root same-vol as source |
| `cmd/server/startup_recovery.go` | Leave resume rows; requeue tasks (already RecoverAllInterrupted) |

---

### Task 1: Resumable CTR encrypt API (Phase 1 foundation)

**Files:**
- Modify: `internal/crypto/ctr.go`, `internal/crypto/envelope.go`
- Test: `internal/crypto/ctr_resume_test.go` (create)

- [ ] **Step 1: Write failing test for resume from offset**

```go
func TestEncryptFileContext_ResumeContinuesCTR(t *testing.T) {
	kek := bytes.Repeat([]byte{0x22}, 32)
	plain := bytes.Repeat([]byte("abcdefghijklmnop"), 64*1024) // 1MiB
	var partial bytes.Buffer
	res1, err := EncryptFileContext(context.Background(), io.LimitReader(bytes.NewReader(plain), 256*1024), &partial, kek)
	// First call must accept optional ResumeState OR use EncryptFileContextResume
	_ = res1
	if err == nil {
		t.Fatal("expected API that can resume; implement EncryptFileContextWithResume")
	}
}
```

Rewrite the test once the API shape below exists (TDD: first assert full round-trip after two-phase encrypt).

Final intended test:

```go
func TestEncryptFileContextWithResume_TwoPhasesMatchFull(t *testing.T) {
	kek := bytes.Repeat([]byte{0x22}, 32)
	plain := bytes.Repeat([]byte{0xAB}, 3<<20) // 3MiB
	var full bytes.Buffer
	want, err := EncryptFileContext(context.Background(), bytes.NewReader(plain), &full, kek)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	st, err := BeginEncryptResume(kek)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteHeader(&out); err != nil {
		t.Fatal(err)
	}
	n1 := int64(1 << 20)
	if err := st.EncryptRange(context.Background(), bytes.NewReader(plain[:n1]), &out, 0, n1); err != nil {
		t.Fatal(err)
	}
	if err := st.EncryptRange(context.Background(), bytes.NewReader(plain[n1:]), &out, n1, int64(len(plain))-n1); err != nil {
		t.Fatal(err)
	}
	got := st.Result()
	if !bytes.Equal(got.WrappedDEK, want.WrappedDEK) && false {
		// WrappedDEK differs if DEK regenerated — resume must REUSE dek/iv from Begin
	}
	dec, err := DecryptStream(bytes.NewReader(out.Bytes()), got.WrappedDEK, kek)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	body, _ := io.ReadAll(dec)
	if !bytes.Equal(body, plain) {
		t.Fatalf("resume decrypt mismatch len=%d", len(body))
	}
}
```

- [ ] **Step 2: Run test — expect fail (types missing)**

Run: `go test ./internal/crypto/ -count=1 -run TestEncryptFileContextWithResume_TwoPhasesMatchFull`

Expected: FAIL compile or missing symbol

- [ ] **Step 3: Implement resume session API**

In `envelope.go` / `ctr.go`:

```go
type EncryptResumeSession struct {
	dek, wrapped []byte
	nonce        [IVSize]byte
	block        cipher.Block
}

func BeginEncryptResume(kek []byte) (*EncryptResumeSession, error) { /* generate dek+nonce, wrap later or now */ }

func (s *EncryptResumeSession) WriteHeader(dst io.Writer) error {
	return writeHeader(dst, ModeCTR, s.nonce[:])
}

// EncryptRange encrypts plainLen bytes from src starting at plainOffset (CTR counter = plainOffset/16).
func (s *EncryptResumeSession) EncryptRange(ctx context.Context, src io.Reader, dst io.Writer, plainOffset, plainLen int64) error {
	if plainOffset%aes.BlockSize != 0 {
		return fmt.Errorf("enc: resume offset must be AES-block aligned: %d", plainOffset)
	}
	stream := cipher.NewCTR(s.block, ctrIV(s.nonce, uint32(plainOffset/aes.BlockSize)))
	// same loop as encryptCTRContext but stop after plainLen bytes
	...
}

func (s *EncryptResumeSession) Result() *EnvelopeResult {
	return &EnvelopeResult{WrappedDEK: s.wrapped, IV: append([]byte(nil), s.nonce[:]...)}
}

func RestoreEncryptResume(kek, wrappedDEK, iv []byte) (*EncryptResumeSession, error) {
	// unwrap DEK, rebuild session for resume after process restart
}
```

Keep existing `EncryptFileContext` as wrapper: `Begin` + `WriteHeader` + `EncryptRange(0, size)`.

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/crypto/ -count=1 -run "Resume|EncryptFileContext"`

- [ ] **Step 5: Commit**

```bash
git add internal/crypto/
git commit -m "feat(crypto): add resumable AES-CTR encrypt sessions"
```

---

### Task 2: `media_encrypt_resume` table + store helpers

**Files:**
- Modify: `internal/store/db.go` (ensure schema on open / community migration list)
- Create: `internal/storage/encrypt_resume.go`
- Test: `internal/storage/encrypt_resume_test.go`

- [ ] **Step 1: Failing test for upsert/load**

```go
func TestEncryptResume_UpsertAndLoad(t *testing.T) {
	db, _ := openStorageTestDB(t) // reuse existing helper pattern from asset_encrypt_test
	EnsureEncryptResumeSchema(db)
	row := EncryptResumeRow{MediaID: 1, Generation: 0, StageID: "s1", EncPath: "/tmp/a.enc", SourcePath: "/tmp/a.mp4",
		SourceIdentity: "id", WrappedDEK: "aa", IV: "bb", PlainOffset: 1 << 20, EncBytesWritten: 1 << 20, State: "encrypting"}
	if err := UpsertEncryptResume(context.Background(), db, row); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEncryptResume(context.Background(), db, 1, 0)
	if err != nil || got.PlainOffset != 1<<20 {
		t.Fatalf("%+v %v", got, err)
	}
}
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/storage/ -count=1 -run TestEncryptResume_UpsertAndLoad`

- [ ] **Step 3: Schema + CRUD**

```sql
CREATE TABLE IF NOT EXISTS media_encrypt_resume (
  media_id INTEGER NOT NULL,
  generation INTEGER NOT NULL,
  stage_id TEXT NOT NULL,
  enc_path TEXT NOT NULL,
  source_path TEXT NOT NULL,
  source_identity TEXT NOT NULL,
  wrapped_dek TEXT NOT NULL,
  iv TEXT NOT NULL,
  plain_offset INTEGER NOT NULL DEFAULT 0 CHECK(plain_offset>=0),
  enc_bytes_written INTEGER NOT NULL DEFAULT 0 CHECK(enc_bytes_written>=0),
  state TEXT NOT NULL CHECK(state IN ('encrypting','staged','abandoned')),
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(media_id, generation)
);
```

```go
func QuickSourceIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	...
	abs, _ := filepath.Abs(path)
	return fmt.Sprintf("%s|%d|%d", filepath.Clean(abs), info.Size(), info.ModTime().UnixNano()), nil
}
```

Checkpoint interval constant: `const EncryptResumeCheckpointBytes = 64 << 20`.

- [ ] **Step 4: Tests PASS + commit**

```bash
git add internal/store/db.go internal/storage/encrypt_resume.go internal/storage/encrypt_resume_test.go
git commit -m "feat(storage): add media_encrypt_resume progress table"
```

---

### Task 3: Wire resumable staging into `StageMediaEncryption`

**Files:**
- Modify: `internal/storage/staged_encrypt.go`
- Test: `internal/storage/staged_encrypt_resume_test.go` (create)
- Modify: `internal/postingest/adapters.go` only if insert journal timing changes (journal still inserted when fully staged)

- [ ] **Step 1: Failing integration-style test**

Create temp plain file ≥2 MiB; call stager that checkpoints every 1 MiB (test seam); cancel after first checkpoint; resume; decrypt equals original.

```go
func TestStageMediaEncryption_ResumesAfterCancel(t *testing.T) {
	// arrange encryptor+db+media row with plain path
	// set checkpointBytes=1<<20 via test seam on AssetEncryptor
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { _, err := enc.StageMediaEncryption(ctx, mediaID); errCh <- err }()
	waitUntilResumeOffset(t, db, mediaID, 1<<20)
	cancel()
	<-errCh
	stage, err := enc.StageMediaEncryption(context.Background(), mediaID)
	if err != nil {
		t.Fatal(err)
	}
	// decrypt stage.EncPath with vault KEK and compare to plain
}
```

- [ ] **Step 2: Implement staging loop**

Replace single `EncryptFileContext` call with:

1. `QuickSourceIdentity(source)`; load resume row if identity matches.
2. `RestoreEncryptResume` or `BeginEncryptResume`; open enc file `O_RDWR` seek or create+header.
3. Loop reading/writing with `EncryptRange`; every `EncryptResumeCheckpointBytes` upsert resume row (`plain_offset`, `enc_bytes_written`).
4. On `ctx.Err()`: upsert offsets, return error **without** deleting enc file.
5. On success: set resume `state=staged`; compute hash once (Phase 1 may still full-hash enc; Phase 2 replaces); return `StagedMediaEncryption` as today for journal insert.

Do **not** call `os.Remove(encPath)` on cancel after a checkpoint exists.

- [ ] **Step 3: Tests PASS**

Run: `go test ./internal/storage/ -count=1 -run StageMediaEncryption_Resumes -timeout 120s`

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(storage): resumable StageMediaEncryption with checkpoints"
```

---

### Task 4: Same-volume quarantine policy (no EXDEV full copy)

**Files:**
- Create: `internal/storage/encrypt_volume.go`
- Modify: `internal/postingest/encryption_quarantine.go`
- Modify: `internal/postingest/adapters.go` (choose quarantine root)
- Test: `internal/postingest/encryption_quarantine_test.go` (extend)

- [ ] **Step 1: Failing test — EXDEV path must not Copy**

Inject ops where `Rename` returns `EXDEV`; assert error like `encryption quarantine refuses cross-volume plaintext copy` and temp file not left as full duplicate on target root.

- [ ] **Step 2: Implement**

```go
func SameVolume(a, b string) (bool, error) { /* Windows: volume name / Unix: stat.dev */ }

func QuarantineRootForSource(sourcePath, preferredRoot string) string {
	// If preferredRoot same volume as source → preferredRoot
	// Else → filepath.Join(filepath.Dir(sourcePath), ".quarantine", "encryption")
}
```

In `quarantinePlaintextWithOps`, on Rename failure:

```go
if isCrossDevice(err) {
	return "", fmt.Errorf("encryption quarantine refuses cross-volume plaintext copy: %w", err)
}
```

Adapter: `quarantineRoot = QuarantineRootForSource(staged.OriginalPath, rootProvider.EncryptionPrivateRoot())`.

- [ ] **Step 3: Tests PASS + commit**

```bash
git commit -m "fix(postingest): refuse cross-volume plaintext quarantine copies"
```

---

### Task 5: Commit preflight without re-hashing entire plaintext

**Files:**
- Modify: `internal/postingest/adapters.go` (`commitEncryptionStage` / identity checks)
- Modify: `internal/storage/staged_encrypt.go` store `source_identity` (quick) in resume; keep full fingerprint optional
- Test: `internal/postingest/encryption_commit_identity_test.go`

- [ ] **Step 1: Test that matching QuickSourceIdentity skips `SourceFingerprint` full hash**

Use a seam counter on fingerprint calls; stage with known identity; commit path should not increment full-hash counter when identity matches.

- [ ] **Step 2: Implement**

At stage time persist `source_identity` (quick). At commit:

```go
cur, err := storage.QuickSourceIdentity(selected)
if err != nil || cur != stagedIdentity {
	return errMismatch
}
// skip publication.SourceFingerprint full rehash when cur matches
```

Where full fingerprint is still required for evidence rows, use the fingerprint captured **once** at stage start (cancellable `SourceFingerprintContext`) and store on `StagedMediaEncryption`, not recompute.

- [ ] **Step 3: PASS + commit**

```bash
git commit -m "perf(postingest): skip full plaintext rehash when encrypt identity matches"
```

---

### Task 6: Unlinked/manual encrypt uses resumable stager

**Files:**
- Modify: `internal/storage/asset_encrypt.go`
- Modify: `internal/postingest/adapters.go` (`encryptUnlinked`)
- Test: `internal/storage/asset_encrypt_resume_test.go`

- [ ] **Step 1: Test manual encrypt cancel+resume via EncryptMediaManual**

- [ ] **Step 2: Refactor `encryptMedia` to call shared `encryptToPathResumable(...)` used by stage + manual** (still run faststart `resolveEncryptSource` for video ISO on manual path).

- [ ] **Step 3: PASS + commit**

```bash
git commit -m "feat(storage): resumable manual EncryptMedia path"
```

---

### Task 7: Phase 1 verification

- [ ] **Step 1: Run focused suites**

```bash
go test ./internal/crypto/ ./internal/storage/ ./internal/postingest/ -count=1 -timeout 300s -run "Resume|Encrypt|Quarantine|StageMedia"
go test ./api/handler/ -count=1 -run EncryptTaskAdmin
```

Expected: PASS

- [ ] **Step 2: Manual checklist (document in PR)**

- Kill mid-encrypt ≥8 GiB fixture → restart → completes without rewriting first half  
- Cross-vol config: no second plaintext on data disk  

- [ ] **Step 3: Commit any fixes; mark Phase 1 done in spec status note**

---

### Task 8: Single-pass ciphertext hash (Phase 2)

**Files:**
- Modify: `internal/crypto/ctr.go` / resume session to optional `hash io.Writer`
- Modify: `internal/storage/staged_encrypt.go` — stop `EncryptionPathHash` full re-read
- Test: `internal/crypto/hash_tee_test.go`

- [ ] **Step 1: Failing test — hash from tee equals `sha256` of enc file**

- [ ] **Step 2: `EncryptRange` writes ciphertext to `io.MultiWriter(dst, sha256)`; expose `Sum()`**

- [ ] **Step 3: PASS + commit**

```bash
git commit -m "perf(crypto): hash ciphertext in the same pass as encrypt"
```

---

### Task 9: Buffer + Sync policy (Phase 2)

**Files:**
- Modify: `internal/crypto/ctr.go` (`ctrEncryptChunk` → 1<<20 or 4<<20)
- Modify: `internal/storage/staged_encrypt.go` — remove eager full Sync after every stage complete **or** Sync only on checkpoint/complete (keep complete Sync before commit)

- [ ] **Step 1: Benchmark or unit asserting chunk size constant**

- [ ] **Step 2: Implement + keep cancel checks between chunks**

- [ ] **Step 3: Commit**

```bash
git commit -m "perf(crypto): larger CTR buffers and deferred stage Sync"
```

---

### Task 10: Faststart before stage + skip if moov-first (Phase 2)

**Files:**
- Modify: `internal/storage/staged_encrypt.go` — call `resolveEncryptSource` when `file_type=video` + ISO
- Modify: `internal/storage/enc_mp4_prepare.go` — if `isoBMFFMoovBeforeMDAT` true, return plainPath without ffmpeg
- Test: `internal/storage/enc_mp4_prepare_test.go` (extend)

- [ ] **Step 1: Failing test — moov-before-mdat fixture skips ffmpeg**

- [ ] **Step 2: Implement skip + wire into StageMediaEncryption**

- [ ] **Step 3: PASS + commit**

```bash
git commit -m "perf(storage): faststart before stage encrypt; skip when already moov-first"
```

---

### Task 11: Phase 2 verification + docs

- [ ] **Step 1: Full relevant tests**

```bash
go test ./internal/crypto/ ./internal/storage/ ./internal/postingest/ -count=1 -timeout 300s
```

- [ ] **Step 2: Update spec status to implemented; short note in plan checkboxes**

- [ ] **Step 3: Community sync note** — all paths under `internal/crypto`, `storage`, `postingest` are Vauldy-safe (no pretranscode)

---

## Spec coverage check

| Spec item | Task |
|-----------|------|
| Resumable CTR offsets | 1, 3, 6 |
| Resume table / journal alternative | 2 |
| Same-vol quarantine / no EXDEV copy | 4 |
| Cancelable / quick identity; no commit rehash | 5 |
| Manual path resume | 6 |
| Single-pass hash | 8 |
| Buffers / Sync policy | 9 |
| Faststart before stage / skip remux | 10 |
| Success criteria / tests | 7, 11 |

## Placeholder scan

No TBD steps; open notes from spec resolved as: **separate `media_encrypt_resume` table**; checkpoint **64 MiB**; Task Manager percent **out of scope**.
