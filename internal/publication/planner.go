package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"knox-media/internal/store"
)

// Planner creates immutable ingest plans using fixed process capabilities and
// library settings read through the caller-owned transaction.
type Planner struct {
	options PlanOptions
}

func NewPlanner(options PlanOptions) *Planner {
	return &Planner{options: options}
}

var ErrGenerationConflict = errors.New("publication planner: generation conflict")

type currentPolicy struct {
	libraryID, generation                      int64
	fileType                                   string
	previewExtract, libraryEncrypt, jitPrepare bool
}

type currentPlan struct {
	mediaID, scanTaskID       int64
	policy                    currentPolicy
	reason                    PlanReason
	preserve                  bool
	metadata                  MetadataAttempt
	required, optional, steps []StepType
	dependencies              []Dependency
	snapshotJSON              []byte
}

func (p *Planner) PlanNewMediaTx(ctx context.Context, tx *sql.Tx, media NewMedia) (Run, error) {
	if tx == nil {
		return Run{}, errors.New("publication planner: nil transaction")
	}
	if media.ScanTaskID <= 0 {
		return Run{}, errors.New("publication planner: invalid scan task id")
	}
	plan, err := p.buildCurrentPolicyTx(ctx, tx, media.MediaID, media.ScanTaskID, PlanReasonScan, false, media.MetadataAttempt)
	if err != nil || plan == nil {
		return Run{}, err
	}
	if strings.TrimSpace(media.FileType) == "" {
		return Run{}, errors.New("publication planner: empty file type hint")
	}
	if strings.TrimSpace(media.FileType) != plan.policy.fileType {
		return Run{}, fmt.Errorf("publication planner: file type hint %q does not match database file type %q", strings.TrimSpace(media.FileType), plan.policy.fileType)
	}
	return p.persistPlanTx(ctx, tx, plan, plan.policy.generation)
}

// PlanReplacementTx creates a fresh generation from current database policy.
// The caller owns tx and is responsible for committing or rolling it back. It
// never copies rows or snapshots from an old run.
func (p *Planner) PlanReplacementTx(ctx context.Context, tx *sql.Tx, mediaID int64, opts ReplacementOptions) (ReplacementResult, error) {
	if tx == nil {
		return ReplacementResult{}, errors.New("publication planner: nil transaction")
	}
	if opts.Reason != PlanReasonRepair && opts.Reason != PlanReasonManualRetry {
		return ReplacementResult{}, fmt.Errorf("publication planner: invalid replacement reason %q", opts.Reason)
	}
	plan, err := p.buildCurrentPolicyTx(ctx, tx, mediaID, 0, opts.Reason, opts.PreserveVisibility, MetadataAttempt{})
	if err != nil || plan == nil {
		return ReplacementResult{}, err
	}
	if plan.policy.generation != opts.ExpectedGeneration {
		return ReplacementResult{}, ErrGenerationConflict
	}
	run, err := p.persistPlanTx(ctx, tx, plan, opts.ExpectedGeneration)
	if err != nil {
		return ReplacementResult{}, err
	}
	if err = SupersedeGenerationTx(ctx, tx, mediaID, opts.ExpectedGeneration, run.Generation); err != nil {
		return ReplacementResult{}, err
	}
	return ReplacementResult{Run: run, OldGeneration: opts.ExpectedGeneration, NewGeneration: run.Generation}, nil
}

// RepairMediaTx remains the compatibility entry point for repair discovery.
func (p *Planner) RepairMediaTx(ctx context.Context, tx *sql.Tx, mediaID int64) (Run, error) {
	if tx == nil {
		return Run{}, errors.New("publication planner: nil transaction")
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, errors.New("publication planner: media or library not found")
		}
		return Run{}, fmt.Errorf("publication planner: load media generation: %w", err)
	}
	result, err := p.PlanReplacementTx(ctx, tx, mediaID, ReplacementOptions{Reason: PlanReasonRepair, PreserveVisibility: true, ExpectedGeneration: generation})
	return result.Run, err
}

func (p *Planner) buildCurrentPolicyTx(ctx context.Context, tx store.SQLExecutor, mediaID, scanTaskID int64, reason PlanReason, preserve bool, metadata MetadataAttempt) (*currentPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("publication planner: nil planner")
	}
	if tx == nil {
		return nil, errors.New("publication planner: nil transaction")
	}
	if mediaID <= 0 {
		return nil, errors.New("publication planner: invalid media id")
	}

	var policy currentPolicy
	var previewExtract, libraryEncrypt, jitPrepare int
	err := tx.QueryRowContext(ctx, `
SELECT m.library_id,COALESCE(m.file_type,''),COALESCE(l.preview_extract,0),
       COALESCE(l.encrypted_assets_enabled,0),COALESCE(l.jit_prepare_on_ingest,0),m.ingest_generation
FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`, mediaID).Scan(
		&policy.libraryID, &policy.fileType, &previewExtract, &libraryEncrypt, &jitPrepare, &policy.generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("publication planner: media or library not found")
	}
	if err != nil {
		return nil, fmt.Errorf("publication planner: load media: %w", err)
	}
	policy.fileType = strings.TrimSpace(policy.fileType)
	policy.previewExtract, policy.libraryEncrypt, policy.jitPrepare = previewExtract == 1, libraryEncrypt == 1, jitPrepare == 1

	if reason == PlanReasonScan {
		var scanLibraryID int64
		err = tx.QueryRowContext(ctx, `SELECT library_id FROM scan_task WHERE id=?`, scanTaskID).Scan(&scanLibraryID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("publication planner: scan task not found")
		}
		if err != nil {
			return nil, fmt.Errorf("publication planner: load scan task: %w", err)
		}
		if scanLibraryID != policy.libraryID {
			return nil, errors.New("publication planner: scan task does not belong to media library")
		}
	}
	if policy.fileType != "video" && policy.fileType != "image" {
		return nil, nil
	}

	required := []StepType{StepPoster}
	if policy.fileType == "image" {
		required = []StepType{StepThumbnail}
	}
	optional := []StepType{StepScrape}
	if policy.fileType == "video" && policy.previewExtract {
		optional = append(optional, StepPreview)
	}
	if policy.fileType == "video" && p.options.SubtitleAuto {
		optional = append(optional, StepSubtitle)
	}
	encrypt := p.options.EncryptGlobal && policy.libraryEncrypt
	if encrypt {
		required = append(required, StepEncrypt)
	}
	prepare := p.options.PreparePlanner != nil && p.options.Capabilities != nil && p.options.Capabilities.Available(string(StepPrepare)) && policy.jitPrepare
	if prepare && policy.fileType == "video" {
		optional = append(optional, StepPrepare)
	}
	steps := append(append([]StepType(nil), required...), optional...)
	dependencies := make([]Dependency, 0, len(steps))
	for _, step := range optional {
		dependencies = append(dependencies, Dependency{Step: step, Kind: DependencyMediaVisible})
	}
	if encrypt {
		dep := required[0]
		dependencies = append(dependencies, Dependency{Step: StepEncrypt, Kind: DependencyStepDone, DependsOn: &dep})
	}

	snapshot := ConfigSnapshot{PolicyVersion: PolicyV2, LibraryID: policy.libraryID, FileType: policy.fileType,
		PreviewExtract: policy.previewExtract, SubtitleAuto: p.options.SubtitleAuto, ATrackAuto: p.options.ATrackAuto,
		Encrypt: encrypt, Prepare: prepare, Steps: append([]StepType(nil), steps...), Metadata: metadata,
		RequiredSteps: append([]StepType(nil), required...), OptionalSteps: append([]StepType(nil), optional...), Dependencies: dependencies}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("publication planner: encode snapshot: %w", err)
	}
	return &currentPlan{mediaID: mediaID, scanTaskID: scanTaskID, policy: policy, reason: reason, preserve: preserve,
		metadata: metadata, required: required, optional: optional, steps: steps, dependencies: dependencies, snapshotJSON: snapshotJSON}, nil
}

func (p *Planner) persistPlanTx(ctx context.Context, tx store.SQLExecutor, plan *currentPlan, expectedGeneration int64) (Run, error) {
	result, err := tx.ExecContext(ctx, `UPDATE media SET ingest_generation=ingest_generation+1,
publication_state=CASE WHEN ? THEN publication_state ELSE 'processing' END,
publication_error=CASE WHEN ? THEN publication_error ELSE '' END
WHERE id=? AND ingest_generation=?`, boolDB(plan.preserve), boolDB(plan.preserve), plan.mediaID, expectedGeneration)
	if err != nil {
		return Run{}, fmt.Errorf("publication planner: advance generation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Run{}, fmt.Errorf("publication planner: read generation CAS: %w", err)
	}
	if affected == 0 {
		return Run{}, ErrGenerationConflict
	}
	generation := expectedGeneration + 1

	result, err = tx.ExecContext(ctx, `INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,preserve_visibility,config_snapshot_json,policy_version)
VALUES(?,?,?,?, 'processing',?,?,?)`, plan.mediaID, generation, nullScanTask(plan.scanTaskID), string(plan.reason), boolDB(plan.preserve), string(plan.snapshotJSON), PolicyV2)
	if err != nil {
		return Run{}, fmt.Errorf("publication planner: insert run: %w", err)
	}
	runID, err := result.LastInsertId()
	if err != nil {
		return Run{}, fmt.Errorf("publication planner: read run id: %w", err)
	}

	stepIDs := make(map[StepType]int64, len(plan.steps))
	for _, step := range plan.steps {
		requiredFlag := 0
		for _, required := range plan.required {
			if required == step {
				requiredFlag = 1
			}
		}
		result, err = tx.ExecContext(ctx, `INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,?,?,?,'waiting')`, runID, plan.mediaID, generation, step, requiredFlag)
		if err != nil {
			return Run{}, fmt.Errorf("publication planner: insert %s step: %w", step, err)
		}
		stepID, err := result.LastInsertId()
		if err != nil {
			return Run{}, fmt.Errorf("publication planner: read %s step id: %w", step, err)
		}
		stepIDs[step] = stepID
		if step == StepPrepare {
			if err = p.options.PreparePlanner.PlanIngestPrepareTx(ctx, tx, plan.mediaID, runID, stepID, generation); err != nil {
				return Run{}, fmt.Errorf("publication planner: enqueue prepare: %w", err)
			}
			continue
		}
		if step == StepScrape {
			_, err = tx.ExecContext(ctx, `INSERT INTO scrape_task(media_id,source,status,progress,ingest_run_id,ingest_step_id,generation) VALUES(?,'auto-scan','waiting',0,?,?,?) ON CONFLICT(ingest_run_id,ingest_step_id,generation) DO NOTHING`, plan.mediaID, runID, stepID, generation)
			if err != nil {
				return Run{}, fmt.Errorf("publication planner: enqueue scrape: %w", err)
			}
			continue
		}
		if !queueBacked(step) {
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO post_ingest_task(media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(?,?,?,?,?,?,'waiting')`, plan.mediaID, nullScanTask(plan.scanTaskID), runID, stepID, generation, step)
		if err != nil {
			return Run{}, fmt.Errorf("publication planner: enqueue %s step: %w", step, err)
		}
	}
	if err := insertDependenciesTx(ctx, tx, plan.dependencies, stepIDs, plan.mediaID, generation, runID); err != nil {
		return Run{}, fmt.Errorf("publication planner: %w", err)
	}
	return Run{ID: runID, MediaID: plan.mediaID, ScanTaskID: plan.scanTaskID, LibraryID: plan.policy.libraryID,
		Generation: generation, State: StateProcessing, Steps: append([]StepType(nil), plan.steps...)}, nil
}

func insertDependenciesTx(ctx context.Context, tx store.SQLExecutor, dependencies []Dependency, stepIDs map[StepType]int64, mediaID, generation, runID int64) error {
	for _, dep := range dependencies {
		stepID, ok := stepIDs[dep.Step]
		if !ok || stepID <= 0 {
			return fmt.Errorf("insert dependency: step %q has no mapped step id", dep.Step)
		}
		var depID any
		if dep.DependsOn != nil {
			mapped, exists := stepIDs[*dep.DependsOn]
			if !exists || mapped <= 0 {
				return fmt.Errorf("insert dependency: target %q has no mapped step id", *dep.DependsOn)
			}
			depID = mapped
		}
		if err := validateDependencyTx(ctx, tx, stepID, depID, mediaID, generation, runID); err != nil {
			return fmt.Errorf("validate dependency %q: %w", dep.Step, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(?,?,?)`, stepID, depID, dep.Kind); err != nil {
			return fmt.Errorf("insert dependency %q: %w", dep.Step, err)
		}
	}
	return nil
}
func validateDependencyTx(ctx context.Context, tx store.SQLExecutor, stepID int64, dependsOn any, mediaID, generation, runID int64) (retErr error) {
	if stepID <= 0 {
		return errors.New("dependency step does not exist")
	}
	var stepRun, stepMedia, stepGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT run_id,media_id,generation FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepRun, &stepMedia, &stepGeneration); err != nil {
		return err
	}
	if stepRun != runID || stepMedia != mediaID || stepGeneration != generation {
		return errors.New("dependency step belongs to a different run/media/generation")
	}
	if dependsOn == nil {
		return nil
	}
	depID, ok := dependsOn.(int64)
	if !ok || depID <= 0 {
		return errors.New("dependency target does not exist")
	}
	if depID == stepID {
		return errors.New("dependency self-edge")
	}
	var depRun, depMedia, depGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT run_id,media_id,generation FROM media_ingest_step WHERE id=?`, depID).Scan(&depRun, &depMedia, &depGeneration); err != nil {
		return err
	}
	if depRun != runID || depMedia != mediaID || depGeneration != generation {
		return errors.New("dependency target belongs to a different run/media/generation")
	}
	graph := map[int64][]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT d.step_id,d.depends_on_step_id FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id WHERE s.run_id=? AND d.dependency_kind='step_done' AND d.depends_on_step_id IS NOT NULL`, runID)
	if err != nil {
		return err
	}
	closed := false
	closeRows := func() error {
		if closed {
			return nil
		}
		closed = true
		return rows.Close()
	}
	defer func() {
		if closeErr := closeRows(); closeErr != nil {
			retErr = errors.Join(retErr, closeErr)
		}
	}()
	for rows.Next() {
		var from, to int64
		if err := rows.Scan(&from, &to); err != nil {
			return err
		}
		graph[from] = append(graph[from], to)
	}
	graph[stepID] = append(graph[stepID], depID)
	visiting, visited := map[int64]bool{}, map[int64]bool{}
	var visit func(int64) bool
	visit = func(node int64) bool {
		if visiting[node] {
			return true
		}
		if visited[node] {
			return false
		}
		visiting[node] = true
		for _, next := range graph[node] {
			if visit(next) {
				return true
			}
		}
		delete(visiting, node)
		visited[node] = true
		return false
	}
	for node := range graph {
		if visit(node) {
			return errors.New("dependency cycle")
		}
	}
	rowsErr := rows.Err()
	if err := closeRows(); err != nil {
		return errors.Join(rowsErr, fmt.Errorf("close dependency rows: %w", err))
	}
	return rowsErr
}

func boolDB(v bool) int {
	if v {
		return 1
	}
	return 0
}
func nullScanTask(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func queueBacked(step StepType) bool {
	switch step {
	case StepPoster, StepThumbnail, StepPreview, StepKeyframe, StepSubtitle, StepAtrack, StepEncrypt:
		return true
	default:
		return false
	}
}
