package pretranscode

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"knox-media/internal/coreiface"
	"knox-media/internal/processmetrics"
	"knox-media/internal/publication"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"time"

	"knox-media/internal/jit/hwenc"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

// Worker runs pretranscode rendition jobs serially with a configurable
// concurrency cap (SRS SCH-01). It is the standalone-mode executor; the
// cluster-mode dispatcher (SRS 3.2.6) is out of scope for this build.
type Worker struct {
	DB           *sql.DB
	Vault        *keystore.Vault
	FFmpegPath   string
	FFprobePath  string
	TranscodeDir string
	MaxCPU       int
	MaxGPU       int

	mu                sync.Mutex
	running           map[int64]context.CancelFunc // jobID -> cancel
	beforeClaimUpdate func()
	parentClaims      map[int64]publication.PrepareParentIdentity
	claimOwner        string
	registry          coreiface.CapabilityRegistry
	semCPU            chan struct{}
	semGPU            chan struct{}
}

// NewWorker constructs a standalone worker. MaxCPU/MaxGPU default to 4/2
// when zero (SRS SCH-01).
func NewWorker(db *sql.DB, vault *keystore.Vault, ffmpegPath, transcodeDir string, maxCPU, maxGPU int, registries ...coreiface.CapabilityRegistry) *Worker {
	if maxCPU <= 0 {
		maxCPU = 4
	}
	if maxGPU <= 0 {
		maxGPU = 2
	}
	return &Worker{
		DB:           db,
		Vault:        vault,
		FFmpegPath:   ffmpegPath,
		TranscodeDir: transcodeDir,
		MaxCPU:       maxCPU,
		MaxGPU:       maxGPU,
		running:      make(map[int64]context.CancelFunc),
		semCPU:       make(chan struct{}, maxCPU),
		semGPU:       make(chan struct{}, maxGPU),
		parentClaims: make(map[int64]publication.PrepareParentIdentity),
		claimOwner:   "pretranscode-" + uuid.NewString(),
		registry: func() coreiface.CapabilityRegistry {
			if len(registries) > 0 {
				return registries[0]
			}
			return nil
		}(),
	}
}

// Start launches the polling loop until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	if _, err := RecoverExpiredPrepareParents(ctx, w.DB, 100); err != nil {
		log.Printf("pretranscode recover expired: %v", err)
	}
	var waitingTasks, waitingJobs int
	_ = w.DB.QueryRow(`SELECT COUNT(1) FROM transcode_task WHERE status='waiting' AND COALESCE(task_type,'batch')='pretranscode'`).Scan(&waitingTasks)
	_ = w.DB.QueryRow(`SELECT COUNT(1) FROM pretranscode_rendition_job WHERE status='waiting'`).Scan(&waitingJobs)
	log.Printf("pretranscode worker started: ffmpeg=%s waiting_tasks=%d waiting_jobs=%d", w.FFmpegPath, waitingTasks, waitingJobs)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := RecoverExpiredPrepareParents(ctx, w.DB, 100); err != nil {
				log.Printf("pretranscode recover expired: %v", err)
			}
			w.runOnce(ctx)
		}
	}
}

// runOnce picks the next eligible waiting rendition job and runs it.
func (w *Worker) runOnce(ctx context.Context) {
	if _, err := w.ProcessNext(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("pretranscode claimNextJob error: %v", err)
	}
}

// ProcessNext claims and starts one eligible rendition. It returns false when
// no work is available; Start uses the same operational path on every tick.
func (w *Worker) ProcessNext(ctx context.Context) (bool, error) {
	job, preset, rendition, mediaID, catalogPath, sourceWidth, sourceHeight, err := w.claimNextJob()
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	if ShouldSkipRenditionAboveSource(*rendition, sourceWidth, sourceHeight) {
		if _, err := w.finalizeJobAndTaskTx(ctx, *job, renditionJobTerminal{Status: "done", Progress: 100, Encoder: "skip"}); err != nil {
			return false, err
		}
		return true, nil
	}
	adapted := AdaptRenditionForSource(*rendition, sourceWidth, sourceHeight)
	rendition = &adapted
	log.Printf("pretranscode job claimed: job_id=%d task_id=%d rendition=%s media_id=%d", job.ID, job.TaskID, rendition.Name, mediaID)
	go w.runRenditionJob(ctx, job, preset, rendition, mediaID, catalogPath)
	return true, nil
}

var ErrJobOwnershipLost = errors.New("pretranscode rendition job ownership lost")

type claimedJob struct {
	ID          int64
	TaskID      int64
	RenditionID int64
	Name        string
	Owner       string
	ParentOwner string
	Parent      publication.PrepareParentIdentity
}

func (w *Worker) setBeforeClaimUpdate(hook func()) {
	w.mu.Lock()
	w.beforeClaimUpdate = hook
	w.mu.Unlock()
}

func (w *Worker) runBeforeClaimUpdate() {
	w.mu.Lock()
	hook := w.beforeClaimUpdate
	w.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (w *Worker) claimNextJob() (*claimedJob, *Preset, *Rendition, int64, string, int, int, error) {
	var parent publication.PrepareParentIdentity
	for {
		w.mu.Lock()
		ids := make([]int64, 0, len(w.parentClaims))
		for id := range w.parentClaims {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if len(ids) > 0 {
			parent = w.parentClaims[ids[0]]
		}
		w.mu.Unlock()
		if parent.TaskID == 0 {
			payload, err := publication.ClaimEligible(context.Background(), w.DB, publication.ClaimRequest{Family: publication.QueuePrepare, TaskType: "prepare", Owner: w.claimOwner, Registry: w.registry})
			if err != nil {
				return nil, nil, nil, 0, "", 0, 0, err
			}
			if payload == nil {
				return nil, nil, nil, 0, "", 0, 0, sql.ErrNoRows
			}
			parent = publication.PrepareParentIdentity{TaskID: payload.QueueID, RunID: payload.RunID.Int64, StepID: payload.StepID.Int64, MediaID: payload.MediaID, Generation: payload.Generation.Int64, Owner: payload.Owner}
			w.mu.Lock()
			w.parentClaims[parent.TaskID] = parent
			w.mu.Unlock()
		}
		var waiting int
		err := w.DB.QueryRow(`SELECT COUNT(*) FROM pretranscode_rendition_job j JOIN transcode_task t ON t.id=j.task_id WHERE j.task_id=? AND j.status='waiting' AND t.status='running' AND t.lease_owner=? AND ((?=0 AND t.ingest_run_id IS NULL AND t.ingest_step_id IS NULL AND t.generation IS NULL) OR (t.ingest_run_id=? AND t.ingest_step_id=? AND t.media_id=? AND t.generation=?))`, parent.TaskID, parent.Owner, parent.RunID, parent.RunID, parent.StepID, parent.MediaID, parent.Generation).Scan(&waiting)
		if err != nil {
			return nil, nil, nil, 0, "", 0, 0, err
		}
		if waiting > 0 {
			break
		}
		w.mu.Lock()
		delete(w.parentClaims, parent.TaskID)
		w.mu.Unlock()
		parent = publication.PrepareParentIdentity{}
	}
	row := w.DB.QueryRow(`SELECT j.id,j.task_id,COALESCE(j.rendition_id,0),j.rendition_name,COALESCE(j.config_snapshot_json,''),COALESCE(t.lease_owner,'') FROM pretranscode_rendition_job j JOIN transcode_task t ON t.id=j.task_id WHERE j.status='waiting' AND COALESCE(j.available_at,CURRENT_TIMESTAMP)<=CURRENT_TIMESTAMP AND t.id=? AND t.status='running' AND t.lease_owner=? AND ((t.ingest_run_id IS NULL AND t.ingest_step_id IS NULL AND t.generation IS NULL) OR (t.ingest_run_id=? AND t.ingest_step_id=? AND t.media_id=? AND t.generation=?)) LIMIT 1`, parent.TaskID, parent.Owner, parent.RunID, parent.StepID, parent.MediaID, parent.Generation)
	var c claimedJob
	var snapshotJSON string
	if err := row.Scan(&c.ID, &c.TaskID, &c.RenditionID, &c.Name, &snapshotJSON, &c.ParentOwner); err != nil {
		return nil, nil, nil, 0, "", 0, 0, err
	}
	c.Owner = "pretranscode/" + uuid.NewString()
	c.Parent = parent
	w.runBeforeClaimUpdate()
	res, err := w.DB.Exec(`UPDATE pretranscode_rendition_job AS j SET status='running',started_at=COALESCE(started_at,CURRENT_TIMESTAMP),lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE id=? AND task_id=? AND status='waiting' AND EXISTS(SELECT 1 FROM transcode_task t WHERE t.id=j.task_id AND t.status='running' AND t.lease_owner=? AND ((t.ingest_run_id IS NULL AND t.ingest_step_id IS NULL AND t.generation IS NULL) OR (t.ingest_run_id=? AND t.ingest_step_id=? AND t.media_id=? AND t.generation=?)))`, c.Owner, c.ID, parent.TaskID, parent.Owner, parent.RunID, parent.StepID, parent.MediaID, parent.Generation)
	if err != nil {
		return nil, nil, nil, 0, "", 0, 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil, nil, 0, "", 0, 0, fmt.Errorf("lost race")
	}
	hydrated := false
	defer func() {
		if !hydrated {
			_, _ = w.DB.Exec(`UPDATE pretranscode_rendition_job SET status='waiting',lease_owner=NULL,lease_until=NULL,started_at=NULL WHERE id=? AND status='running' AND lease_owner=?`, c.ID, c.Owner)
		}
	}()
	var linkedGeneration int64
	if err = w.DB.QueryRow(`SELECT COALESCE(generation,0) FROM transcode_task WHERE id=?`, c.TaskID).Scan(&linkedGeneration); err != nil {
		return nil, nil, nil, 0, "", 0, 0, err
	}
	if linkedGeneration > 0 && snapshotJSON == "" {
		return nil, nil, nil, 0, "", 0, 0, fmt.Errorf("ingest prepare immutable snapshot missing for job %d", c.ID)
	}
	// Linked ingest jobs are hydrated only from their immutable execution snapshot.
	if snapshotJSON != "" {
		var snapshot ingestPrepareJobSnapshot
		if err = json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
			return nil, nil, nil, 0, "", 0, 0, fmt.Errorf("decode ingest prepare snapshot for job %d: %w", c.ID, err)
		}
		var mediaID, libraryID, sourceWidth, sourceHeight int64
		var catalogPath string
		_ = w.DB.QueryRow(`SELECT COALESCE(m.id,0),COALESCE(m.library_id,0),COALESCE(m.width,0),COALESCE(m.height,0),COALESCE(m.file_path,'') FROM transcode_task t LEFT JOIN media m ON m.file_id=t.file_id WHERE t.id=?`, c.TaskID).Scan(&mediaID, &libraryID, &sourceWidth, &sourceHeight, &catalogPath)
		if catalogPath == "" {
			return nil, nil, nil, 0, "", 0, 0, fmt.Errorf("input path missing for task %d", c.TaskID)
		}
		if libraryID > 0 {
			if resolved := storage.ResolveMediaAbsolutePath(w.DB, libraryID, catalogPath); resolved != "" {
				catalogPath = resolved
			}
		}
		hydrated = true
		return &c, &snapshot.Preset, &snapshot.Rendition, mediaID, catalogPath, int(sourceWidth), int(sourceHeight), nil
	}
	// Legacy manual jobs retain mutable preset hydration.
	var p Preset
	var presetID int64
	var encMode, outFormat, videoCodec, videoPreset, videoMaxrate, videoBufsize, videoProfile, videoPixFmt, audioCodec, audioBitrate string
	var videoCRF, videoGOP, audioCh, audioSR, hwFB int
	err = w.DB.QueryRow(`SELECT pt.preset_id, pt.output_format, pt.encryption_mode,
		p.video_codec, COALESCE(p.video_preset,''), COALESCE(p.video_crf,0), COALESCE(p.video_maxrate,''),
		COALESCE(p.video_bufsize,''), COALESCE(p.video_profile,''), COALESCE(p.video_gop,0), COALESCE(p.video_pix_fmt,''),
		COALESCE(NULLIF(p.audio_codec,''),'aac'), p.audio_bitrate, COALESCE(p.audio_channels,2), COALESCE(p.audio_sample_rate,48000), COALESCE(p.hw_fallback,1)
		FROM pretranscode_task_meta pt
		JOIN transcode_preset p ON p.id = pt.preset_id
		WHERE pt.task_id = ?`, c.TaskID).Scan(&presetID, &outFormat, &encMode,
		&videoCodec, &videoPreset, &videoCRF, &videoMaxrate, &videoBufsize, &videoProfile, &videoGOP, &videoPixFmt,
		&audioCodec, &audioBitrate, &audioCh, &audioSR, &hwFB)
	if err != nil {
		return nil, nil, nil, 0, "", 0, 0, err
	}
	p.ID = presetID
	p.OutputFormat = outFormat
	p.EncryptionMode = encMode
	p.VideoCodec = videoCodec
	p.VideoPreset = videoPreset
	p.VideoCRF = videoCRF
	p.VideoMaxrate = videoMaxrate
	p.VideoBufsize = videoBufsize
	p.VideoProfile = videoProfile
	p.VideoGOP = videoGOP
	p.VideoPixFmt = videoPixFmt
	p.AudioCodec = audioCodec
	p.AudioBitrate = audioBitrate
	p.AudioChannels = audioCh
	p.AudioSampleRate = audioSR
	p.HWFallback = hwFB == 1

	var r Rendition
	err = w.DB.QueryRow(`SELECT id, preset_id, name, height, COALESCE(width,0), video_bitrate, COALESCE(audio_bitrate,''), COALESCE(bandwidth,0), COALESCE(sort_order,0)
		FROM preset_rendition WHERE id = ?`, c.RenditionID).Scan(&r.ID, &r.PresetID, &r.Name, &r.Height, &r.Width, &r.VideoBitrate, &r.AudioBitrate, &r.Bandwidth, &r.SortOrder)
	if err != nil {
		return nil, nil, nil, 0, "", 0, 0, err
	}

	var mediaID int64
	var libraryID int64
	var catalogPath string
	var sourceWidth, sourceHeight int
	_ = w.DB.QueryRow(`SELECT COALESCE(m.id,0), COALESCE(m.library_id,0), COALESCE(m.width,0), COALESCE(m.height,0), COALESCE(m.file_path,'')
		FROM transcode_task t LEFT JOIN media m ON m.file_id = t.file_id WHERE t.id = ?`, c.TaskID).Scan(&mediaID, &libraryID, &sourceWidth, &sourceHeight, &catalogPath)
	if catalogPath == "" {
		return nil, nil, nil, 0, "", 0, 0, fmt.Errorf("input path missing for task %d", c.TaskID)
	}
	if libraryID > 0 {
		if resolved := storage.ResolveMediaAbsolutePath(w.DB, libraryID, catalogPath); resolved != "" {
			catalogPath = resolved
		}
	}
	hydrated = true
	return &c, &p, &r, mediaID, catalogPath, sourceWidth, sourceHeight, nil
}

// runRenditionJob executes FFmpeg and updates progress.
func (w *Worker) runRenditionJob(parent context.Context, job *claimedJob, p *Preset, r *Rendition, mediaID int64, catalogPath string) {
	ctx, cancel := context.WithCancel(parent)
	w.mu.Lock()
	w.running[job.ID] = cancel
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.running, job.ID)
		w.mu.Unlock()
		cancel()
	}()
	leaseDone := make(chan struct{})
	leaseLost := make(chan error, 1)
	go w.renewJobLeaseLoop(ctx, *job, leaseDone, cancel, leaseLost)
	defer close(leaseDone)

	// Acquire concurrency slot based on encoder family.
	availableEncoders := hwenc.ListAvailableEncoders(w.FFmpegPath)
	encoder := ResolveEncoder(p, availableEncoders, w.FFmpegPath)
	if isHardwareEncoder(encoder) {
		w.semGPU <- struct{}{}
		defer func() { <-w.semGPU }()
	} else {
		w.semCPU <- struct{}{}
		defer func() { <-w.semCPU }()
	}

	// Mark task running.
	_, _ = w.DB.Exec(`UPDATE transcode_task SET status='running', started_at=CURRENT_TIMESTAMP WHERE id=? AND status='waiting'`, job.TaskID)

	var taskRoot string
	_ = w.DB.QueryRow(`SELECT COALESCE(output_path,'') FROM pretranscode_task_meta WHERE task_id=?`, job.TaskID).Scan(&taskRoot)
	if taskRoot == "" {
		taskRoot = filepath.Join(w.TranscodeDir, p.OutputFormat, fmt.Sprintf("task%d", job.TaskID))
	}
	outDir := RenditionOutputDir(taskRoot, r.Name)
	var keyInfo *KeyInfo
	if p.EncryptionMode == "aes128" {
		ki, err := GenerateAES128KeyInfo(outDir, job.TaskID, "")
		if err != nil {
			w.failJob(job, fmt.Sprintf("aes128 key gen: %v", err), encoder)
			return
		}
		keyInfo = ki
		// Persist key material to drm_key_material (SRS ENC-06).
		w.persistAESKey(job.TaskID, ki)
	}

	var built FFmpegArgs
	switch p.OutputFormat {
	case "mp4":
		built = BuildMP4Args(outDir, p, r, encoder)
	case "hls":
		built = BuildHLSArgs(outDir, p, r, keyInfo, encoder)
	default:
		// DASH/FLV reserved for a later phase; treat as HLS for now.
		built = BuildHLSArgs(outDir, p, r, keyInfo, encoder)
	}

	ffmpegIn, err := storage.OpenFFmpegInput(w.DB, w.Vault, mediaID, catalogPath, 0)
	if err != nil {
		w.failJob(job, fmt.Sprintf("open input: %v", err), encoder)
		return
	}
	if ffmpegIn.Cleanup != nil {
		defer ffmpegIn.Cleanup()
	}

	global := FFmpegGlobalOpts()
	post := built.Args
	if len(post) > len(global) {
		post = post[len(global):]
	} else {
		post = nil
	}
	fullArgs, stdin := storage.ApplyFFmpegInput(append([]string{}, global...), ffmpegIn)
	fullArgs = append(fullArgs, post...)
	fullArgs = append(fullArgs, "-progress", "pipe:1")

	cmd := processmetrics.NewFFmpegCommandContext(ctx, w.FFmpegPath, fullArgs...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		w.failJob(job, fmt.Sprintf("stdout pipe: %v", err), encoder)
		return
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		w.failJob(job, fmt.Sprintf("ffmpeg start: %v", err), encoder)
		return
	}
	// Parse progress (SRS 6.8).
	durationUS := w.lookupDurationUS(job.TaskID)
	done := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "out_time_us=") {
				v, _ := strconv.ParseInt(strings.TrimPrefix(line, "out_time_us="), 10, 64)
				if durationUS > 0 {
					pct := int(v * 100 / durationUS)
					if pct < 0 {
						pct = 0
					}
					if pct > 99 {
						pct = 99
					}
					if err := w.updateJobProgress(ctx, *job, pct); err != nil {
						cancel()
						return
					}
					w.refreshTaskProgress(job.TaskID)
				}
			}
		}
		close(done)
	}()
	err = cmd.Wait()
	<-done
	select {
	case <-leaseLost:
		return
	default:
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		w.failJob(job, trimErr(err, stderr.String()), encoder)
		return
	}
	// Verify output exists (SRS 9.2).
	if _, statErr := os.Stat(built.OutFile); statErr != nil {
		w.failJob(job, "output file missing after ffmpeg success", encoder)
		return
	}
	if _, err := w.finalizeJobAndTaskTx(ctx, *job, renditionJobTerminal{Status: "done", Progress: 100, OutputPath: built.OutFile, Encoder: encoder}); err != nil {
		log.Printf("pretranscode job %d finalize failed: %v", job.ID, err)
	}
}

// CancelRendition cancels a running job (SRS ACT-01).
func (w *Worker) CancelRendition(jobID int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if c, ok := w.running[jobID]; ok {
		c()
		return true
	}
	return false
}

func (w *Worker) failJob(job *claimedJob, msg, encoder string) {
	if _, err := w.finalizeJobAndTaskTx(context.Background(), *job, renditionJobTerminal{Status: "failed", ErrorMessage: truncate(msg, 1600), Encoder: encoder}); err != nil {
		log.Printf("pretranscode job %d failure finalize failed: %v", job.ID, err)
	}
	log.Printf("pretranscode job %d failed: %s", job.ID, msg)
}

func (w *Worker) refreshTaskProgress(taskID int64) {
	jobs, err := (&TaskService{DB: w.DB, TranscodeDir: w.TranscodeDir}).ListRenditionJobs(taskID)
	if err != nil || len(jobs) == 0 {
		return
	}
	overall := AggregateProgress(jobs)
	_, _ = w.DB.Exec(`UPDATE transcode_task SET progress=? WHERE id=?`, overall, taskID)
}

func (w *Worker) maybeCompleteTask(taskID int64) { _ = w.finalizeTask(context.Background(), taskID) }

func (w *Worker) maybeFailTask(taskID int64) { _ = w.finalizeTask(context.Background(), taskID) }

func (w *Worker) renewJobLease(ctx context.Context, job claimedJob) error {
	p := job.Parent
	if p.TaskID <= 0 || p.Owner == "" {
		return ErrJobOwnershipLost
	}
	_, err := store.WithImmediateConnTx(ctx, w.DB, func(tx store.ImmediateConnTx) error {
		pred := `EXISTS(SELECT 1 FROM transcode_task t JOIN media_ingest_run r ON r.id=t.ingest_run_id JOIN media_ingest_step s ON s.id=t.ingest_step_id JOIN media m ON m.id=t.media_id WHERE t.id=? AND t.status='running' AND t.lease_owner=? AND t.ingest_run_id=? AND t.ingest_step_id=? AND t.media_id=? AND t.generation=? AND r.id=? AND r.media_id=? AND r.generation=? AND r.status='processing' AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND s.id=? AND s.run_id=? AND s.media_id=? AND s.generation=? AND s.status='running' AND s.lease_owner=? AND m.id=? AND m.ingest_generation=?)`
		args := []any{p.TaskID, p.Owner, p.RunID, p.StepID, p.MediaID, p.Generation, p.RunID, p.MediaID, p.Generation, p.StepID, p.RunID, p.MediaID, p.Generation, p.Owner, p.MediaID, p.Generation}
		r, e := tx.ExecContext(ctx, `UPDATE pretranscode_rendition_job SET lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE id=? AND task_id=? AND status='running' AND lease_owner=? AND `+pred, append([]any{job.ID, job.TaskID, job.Owner}, args...)...)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return ErrJobOwnershipLost
		}
		r, e = tx.ExecContext(ctx, `UPDATE transcode_task SET lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE id=? AND status='running' AND lease_owner=? AND ingest_run_id=? AND ingest_step_id=? AND media_id=? AND generation=?`, p.TaskID, p.Owner, p.RunID, p.StepID, p.MediaID, p.Generation)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return ErrJobOwnershipLost
		}
		r, e = tx.ExecContext(ctx, `UPDATE media_ingest_step SET lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE id=? AND run_id=? AND media_id=? AND generation=? AND status='running' AND lease_owner=?`, p.StepID, p.RunID, p.MediaID, p.Generation, p.Owner)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return ErrJobOwnershipLost
		}
		return nil
	})
	return err
}

func (w *Worker) renewJobLeaseLoop(ctx context.Context, job claimedJob, done <-chan struct{}, cancel context.CancelFunc, lost chan<- error) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if err := w.renewJobLease(ctx, job); err != nil {
				select {
				case lost <- err:
				default:
				}
				cancel()
				return
			}
		}
	}
}

func (w *Worker) updateJobProgress(ctx context.Context, job claimedJob, progress int) error {
	p := job.Parent
	if p.TaskID <= 0 || p.Owner == "" {
		return ErrJobOwnershipLost
	}
	r, err := w.DB.ExecContext(ctx, `UPDATE pretranscode_rendition_job AS j SET progress=?,lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE j.id=? AND j.task_id=? AND j.status='running' AND j.lease_owner=? AND EXISTS(SELECT 1 FROM transcode_task t JOIN media_ingest_run r ON r.id=t.ingest_run_id JOIN media_ingest_step s ON s.id=t.ingest_step_id JOIN media m ON m.id=t.media_id WHERE t.id=j.task_id AND t.status='running' AND t.lease_owner=? AND t.ingest_run_id=? AND t.ingest_step_id=? AND t.media_id=? AND t.generation=? AND r.status='processing' AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND s.status='running' AND s.lease_owner=? AND m.ingest_generation=?)`, progress, job.ID, job.TaskID, job.Owner, p.Owner, p.RunID, p.StepID, p.MediaID, p.Generation, p.Owner, p.Generation)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return ErrJobOwnershipLost
	}
	return nil
}

type renditionJobTerminal struct {
	Status       string
	Progress     int
	OutputPath   string
	ErrorMessage string
	Encoder      string
}

// finalizeJobAndTaskTx persists one running rendition's terminal payload and,
// when it is the final active rendition, synchronizes task and publication state
// in the same transaction. A downstream failure rolls back the job transition.
func (w *Worker) finalizeJobAndTaskTx(ctx context.Context, job claimedJob, terminal renditionJobTerminal) (bool, error) {
	if job.Parent.TaskID <= 0 || job.Parent.Owner == "" {
		return false, ErrJobOwnershipLost
	}
	if terminal.Status != "done" && terminal.Status != "failed" {
		return false, fmt.Errorf("pretranscode job %d invalid terminal status %q", job.ID, terminal.Status)
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE pretranscode_rendition_job SET status=?,progress=?,output_path=?,error_message=?,encoder_used=?,completed_at=CURRENT_TIMESTAMP WHERE id=? AND task_id=? AND status='running' AND lease_owner=? AND EXISTS(SELECT 1 FROM transcode_task t JOIN media_ingest_run r ON r.id=t.ingest_run_id JOIN media_ingest_step s ON s.id=t.ingest_step_id JOIN media m ON m.id=t.media_id WHERE t.id=pretranscode_rendition_job.task_id AND t.status='running' AND t.lease_owner=? AND t.ingest_run_id=? AND t.ingest_step_id=? AND t.media_id=? AND t.generation=? AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND s.status='running' AND s.lease_owner=? AND m.ingest_generation=?)`, terminal.Status, terminal.Progress, terminal.OutputPath, terminal.ErrorMessage, terminal.Encoder, job.ID, job.TaskID, job.Owner, job.Parent.Owner, job.Parent.RunID, job.Parent.StepID, job.Parent.MediaID, job.Parent.Generation, job.Parent.Owner, job.Parent.Generation)
	if err != nil {
		return false, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, ErrJobOwnershipLost
	}
	var total, done, failed, pending, progress int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(1),COALESCE(SUM(status='done'),0),COALESCE(SUM(status='failed'),0),COALESCE(SUM(status IN ('waiting','running')),0),COALESCE(SUM(progress),0) FROM pretranscode_rendition_job WHERE task_id=?`, job.TaskID).Scan(&total, &done, &failed, &pending, &progress); err != nil {
		return false, err
	}
	if total == 0 {
		return false, fmt.Errorf("pretranscode task %d has no rendition jobs", job.TaskID)
	}
	if pending > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE transcode_task SET progress=? WHERE id=? AND status IN ('waiting','running')`, progress/total, job.TaskID); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	status := "done"
	if failed > 0 {
		status = "failed"
	}
	var outputPath string
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(output_path,'') FROM pretranscode_task_meta WHERE task_id=?`, job.TaskID).Scan(&outputPath)
	res, err = tx.ExecContext(ctx, `UPDATE transcode_task SET progress=CASE WHEN ?='done' THEN 100 ELSE progress END,output_path=CASE WHEN ?='done' THEN ? ELSE output_path END WHERE id=? AND status IN ('waiting','running')`, status, status, outputPath, job.TaskID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, fmt.Errorf("pretranscode task %d not active", job.TaskID)
	}
	if err = completeLinkedPrepareTx(ctx, tx, job.Parent, status); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	w.mu.Lock()
	delete(w.parentClaims, job.Parent.TaskID)
	w.mu.Unlock()
	_ = done
	if wh := CurrentWebhookDispatcher(); wh != nil {
		if status == "done" {
			wh.SendTaskCompleted(job.TaskID)
		} else {
			wh.SendTaskFailed(job.TaskID)
		}
	}
	return true, nil
}

func completeLinkedPrepareTx(ctx context.Context, tx *sql.Tx, parent publication.PrepareParentIdentity, status string) error {
	taskID := parent.TaskID
	var runID, stepID, generation, mediaID sql.NullInt64
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT ingest_run_id,ingest_step_id,generation,media_id,COALESCE(lease_owner,'') FROM transcode_task WHERE id=?`, taskID).Scan(&runID, &stepID, &generation, &mediaID, &owner); err != nil {
		return err
	}
	if !runID.Valid && !stepID.Valid && !generation.Valid {
		return nil
	}
	if !runID.Valid || !stepID.Valid || !generation.Valid {
		return fmt.Errorf("pretranscode task %d has partial ingest linkage", taskID)
	}
	lastError := ""
	if status == "failed" {
		lastError = "pretranscode rendition failed"
	}
	if err := publication.CompletePrepareTx(ctx, tx, publication.PrepareParentIdentity{TaskID: taskID, RunID: runID.Int64, StepID: stepID.Int64, MediaID: mediaID.Int64, Generation: generation.Int64, Owner: parent.Owner}, status == "done", lastError); err != nil {
		return fmt.Errorf("pretranscode task %d complete linked prepare: %w", taskID, err)
	}
	return nil
}

// finalizeTask atomically synchronizes a terminal pretranscode task with its
// linked prepare step and publication aggregate. Unlinked legacy tasks retain
// their existing lifecycle without touching publication state.
func (w *Worker) finalizeTask(ctx context.Context, taskID int64) error {
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var total, done, failed, pending int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(1),COALESCE(SUM(status='done'),0),COALESCE(SUM(status='failed'),0),COALESCE(SUM(status IN ('waiting','running')),0) FROM pretranscode_rendition_job WHERE task_id=?`, taskID).Scan(&total, &done, &failed, &pending); err != nil {
		return err
	}
	if total == 0 || pending > 0 {
		return nil
	}
	status := "done"
	if failed > 0 {
		status = "failed"
	}
	var outputPath string
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(output_path,'') FROM pretranscode_task_meta WHERE task_id=?`, taskID).Scan(&outputPath)
	res, err := tx.ExecContext(ctx, `UPDATE transcode_task SET progress=CASE WHEN ?='done' THEN 100 ELSE progress END,output_path=CASE WHEN ?='done' THEN ? ELSE output_path END WHERE id=? AND status NOT IN ('done','failed')`, status, status, outputPath, taskID)
	if err != nil {
		return err
	}
	changed, _ := res.RowsAffected()
	var runID, stepID, generation, mediaID sql.NullInt64
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT ingest_run_id,ingest_step_id,generation,media_id,COALESCE(lease_owner,'') FROM transcode_task WHERE id=?`, taskID).Scan(&runID, &stepID, &generation, &mediaID, &owner); err != nil {
		return err
	}
	if runID.Valid || stepID.Valid || generation.Valid {
		if !runID.Valid || !stepID.Valid || !generation.Valid {
			return fmt.Errorf("pretranscode task %d has partial ingest linkage", taskID)
		}
		lastError := ""
		if status == "failed" {
			lastError = "pretranscode rendition failed"
		}
		if err = publication.CompletePrepareTx(ctx, tx, publication.PrepareParentIdentity{TaskID: taskID, RunID: runID.Int64, StepID: stepID.Int64, MediaID: mediaID.Int64, Generation: generation.Int64, Owner: owner}, status == "done", lastError); err != nil {
			return fmt.Errorf("pretranscode task %d complete linked prepare: %w", taskID, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if changed > 0 {
		if wh := CurrentWebhookDispatcher(); wh != nil {
			if status == "done" {
				wh.SendTaskCompleted(taskID)
			} else {
				wh.SendTaskFailed(taskID)
			}
		}
	}
	_ = done
	return nil
}

func (w *Worker) lookupDurationUS(taskID int64) int64 {
	var dur sql.NullInt64
	_ = w.DB.QueryRow(`SELECT m.duration FROM transcode_task t LEFT JOIN media m ON m.file_id = t.file_id WHERE t.id=?`, taskID).Scan(&dur)
	if dur.Valid && dur.Int64 > 0 {
		return dur.Int64 * 1000000 // media.duration is in seconds.
	}
	return 0
}

func (w *Worker) persistAESKey(taskID int64, ki *KeyInfo) {
	// Best-effort insert into drm_key_material (community table).
	var fileID string
	_ = w.DB.QueryRow(`SELECT file_id FROM transcode_task WHERE id=?`, taskID).Scan(&fileID)
	var mediaID sql.NullInt64
	_ = w.DB.QueryRow(`SELECT id FROM media WHERE file_id=?`, fileID).Scan(&mediaID)
	if !mediaID.Valid {
		return
	}
	_, _ = w.DB.Exec(`INSERT OR REPLACE INTO drm_key_material (media_id, mode, kid, key_hex, iv_hex) VALUES (?, 'aes128', ?, ?, ?)`,
		mediaID.Int64, fmt.Sprintf("pretranscode-task%d", taskID), ki.KeyHex, ki.IVHex)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "...[truncated]\n" + s[len(s)-max:]
}

func trimErr(err error, stderr string) string {
	stderr = strings.TrimSpace(stderr)
	const m = 1600
	if len(stderr) > m {
		stderr = "...[stderr truncated]\n" + stderr[len(stderr)-m:]
	}
	return fmt.Sprintf("ffmpeg: %v; stderr: %s", err, stderr)
}

// GlobalWebhookDispatcher is set by the module to dispatch webhook events
// from worker callbacks. Nil in tests that don't care about webhooks.
var webhookDispatcherMu sync.RWMutex
var GlobalWebhookDispatcher WebhookDispatcher

func CurrentWebhookDispatcher() WebhookDispatcher {
	webhookDispatcherMu.RLock()
	defer webhookDispatcherMu.RUnlock()
	return GlobalWebhookDispatcher
}
func setWebhookDispatcher(dispatcher WebhookDispatcher) {
	webhookDispatcherMu.Lock()
	GlobalWebhookDispatcher = dispatcher
	webhookDispatcherMu.Unlock()
}

// WebhookDispatcher is the minimal interface the worker calls.
type WebhookDispatcher interface {
	SendTaskCompleted(taskID int64)
	SendTaskFailed(taskID int64)
	SendRenditionCompleted(taskID int64, jobID int64, renditionName string)
}
