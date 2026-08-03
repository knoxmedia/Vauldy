package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"knox-media/internal/libraryprocessing"
	"knox-media/internal/store"
	"knox-media/internal/taskalign"
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
	libraryID, generation                                        int64
	fileType                                                     string
	explicit                                                     libraryprocessing.Options
	libraryEncrypt, jitPrepare, cleanupPackage, cleanupEncrypted bool
}

type currentPlan struct {
	mediaID, scanTaskID, ingestItemID int64
	policy                           currentPolicy
	reason                           PlanReason
	preserve                         bool
	metadata                         MetadataAttempt
	required, optional, steps        []StepType
	dependencies                     []Dependency
	graph                            PlanGraph
	snapshotJSON                     []byte
}

func (p *Planner) PlanNewMediaTx(ctx context.Context, tx *sql.Tx, media NewMedia) (Run, error) {
	if tx == nil {
		return Run{}, errors.New("publication planner: nil transaction")
	}
	reason := PlanReasonScan
	if media.ScanTaskID <= 0 && media.IngestItemID > 0 {
		reason = PlanReasonUpload
	}
	if media.ScanTaskID <= 0 && media.IngestItemID <= 0 {
		return Run{}, errors.New("publication planner: invalid scan task id")
	}
	plan, err := p.buildCurrentPolicyTx(ctx, tx, media.MediaID, media.ScanTaskID, reason, false, media.MetadataAttempt)
	if err != nil || plan == nil {
		return Run{}, err
	}
	if strings.TrimSpace(media.FileType) == "" {
		return Run{}, errors.New("publication planner: empty file type hint")
	}
	if strings.TrimSpace(media.FileType) != plan.policy.fileType {
		return Run{}, fmt.Errorf("publication planner: file type hint %q does not match database file type %q", strings.TrimSpace(media.FileType), plan.policy.fileType)
	}
	plan.ingestItemID = media.IngestItemID
	return p.persistPlanTx(ctx, tx, plan, plan.policy.generation)
}

// PlanReplacementTx creates a fresh generation from current database policy.
// The caller owns tx and is responsible for committing or rolling it back. It
// never copies rows or snapshots from an old run.
func (p *Planner) PlanReplacementTx(ctx context.Context, tx *sql.Tx, mediaID int64, opts ReplacementOptions) (ReplacementResult, error) {
	if tx == nil {
		return ReplacementResult{}, errors.New("publication planner: nil transaction")
	}
	return p.planReplacement(ctx, tx, mediaID, opts)
}

func (p *Planner) planReplacement(ctx context.Context, tx store.SQLExecutor, mediaID int64, opts ReplacementOptions) (ReplacementResult, error) {
	if tx == nil {
		return ReplacementResult{}, errors.New("publication planner: nil transaction")
	}
	if opts.Reason != PlanReasonRepair && opts.Reason != PlanReasonManualRetry && opts.Reason != PlanReasonSourceReplaced {
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
	if err = supersedeGeneration(ctx, tx, mediaID, opts.ExpectedGeneration, run.Generation); err != nil {
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
	var preview, subtitleExtract, atrackExtract, recognize, keyframe, ai, encrypted, prepare, cleanupPackage, cleanupEncrypted int
	var lyricRecognize, audioAnalysis, photoClassify, photoGeocode, photoFace, imageOCR, documentConvert, documentFulltext int
	err := tx.QueryRowContext(ctx, `SELECT m.library_id,COALESCE(m.file_type,''),COALESCE(l.preview_extract,0),COALESCE(l.subtitle_extract,0),COALESCE(l.atrack_extract,0),COALESCE(l.subtitle_recognize,0),COALESCE(l.keyframe_extract,0),COALESCE(l.ai_analysis,0),COALESCE(l.lyric_recognize,0),COALESCE(l.audio_analysis,0),COALESCE(l.photo_classify,0),COALESCE(l.photo_geocode,0),COALESCE(l.photo_face,0),COALESCE(l.image_ocr,0),COALESCE(l.document_convert,0),COALESCE(l.document_fulltext,0),COALESCE(l.encrypted_assets_enabled,0),COALESCE(l.jit_prepare_on_ingest,0),COALESCE(l.cleanup_local_source_after_package,0),COALESCE(l.encrypted_assets_cleanup_plaintext,0),m.ingest_generation FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`, mediaID).Scan(&policy.libraryID, &policy.fileType, &preview, &subtitleExtract, &atrackExtract, &recognize, &keyframe, &ai, &lyricRecognize, &audioAnalysis, &photoClassify, &photoGeocode, &photoFace, &imageOCR, &documentConvert, &documentFulltext, &encrypted, &prepare, &cleanupPackage, &cleanupEncrypted, &policy.generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("publication planner: media or library not found")
	}
	if err != nil {
		return nil, fmt.Errorf("publication planner: load media: %w", err)
	}
	policy.fileType = strings.TrimSpace(policy.fileType)
	policy.explicit = libraryprocessing.Options{Preview: preview == 1, SubtitleExtract: subtitleExtract == 1, ATrackExtract: atrackExtract == 1, SubtitleRecognize: recognize == 1, KeyframeExtract: keyframe == 1, AIAnalysis: ai == 1, LyricRecognize: lyricRecognize == 1, AudioAnalysis: audioAnalysis == 1, PhotoClassify: photoClassify == 1, PhotoGeocode: photoGeocode == 1, PhotoFace: photoFace == 1, ImageOCR: imageOCR == 1, DocumentConvert: documentConvert == 1, DocumentFulltext: documentFulltext == 1}
	policy.libraryEncrypt = encrypted == 1
	policy.jitPrepare = prepare == 1
	policy.cleanupPackage = cleanupPackage == 1
	policy.cleanupEncrypted = cleanupEncrypted == 1
	if reason == PlanReasonScan {
		var libraryID int64
		err = tx.QueryRowContext(ctx, `SELECT library_id FROM scan_task WHERE id=?`, scanTaskID).Scan(&libraryID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("publication planner: scan task not found")
		}
		if err != nil {
			return nil, fmt.Errorf("publication planner: load scan task: %w", err)
		}
		if libraryID != policy.libraryID {
			return nil, errors.New("publication planner: scan task does not belong to media library")
		}
	}
	if policy.fileType != "video" && policy.fileType != "image" && !isMediaCategory(policy.fileType) {
		return nil, nil
	}
	effective, provenance := libraryprocessing.Close(policy.fileType, policy.explicit)
	if provenance.Explicit == nil {
		provenance.Explicit = []string{}
	}
	if provenance.DependencyAdded == nil {
		provenance.DependencyAdded = []string{}
	}
	var legacyDefaults []string
	if p.options.SubtitleAuto && !effective.SubtitleExtract {
		effective.SubtitleExtract = true
		legacyDefaults = append(legacyDefaults, libraryprocessing.OptionSubtitleExtract)
	}
	if p.options.ATrackAuto && !effective.ATrackExtract {
		effective.ATrackExtract = true
		legacyDefaults = append(legacyDefaults, libraryprocessing.OptionATrackExtract)
	}
	if policy.fileType != "video" {
		policy.explicit = libraryprocessing.Options{}
		effective, provenance = libraryprocessing.Close(policy.fileType, policy.explicit)
	}
	if provenance.Explicit == nil {
		provenance.Explicit = []string{}
	}
	if provenance.DependencyAdded == nil {
		provenance.DependencyAdded = []string{}
	}
	for _, step := range []StepType{StepSubtitleRecognize, StepAIAnalysis} {
		selected := step == StepSubtitleRecognize && effective.SubtitleRecognize || step == StepAIAnalysis && effective.AIAnalysis
		if selected && !hasExecutableAdapter(p.options.ExecutableAdapters, step) {
			return nil, fmt.Errorf("%w: executable adapter unavailable for %s under policy v%d", ErrCapabilityUnavailable, step, CurrentPolicyVersion)
		}
	}
	tmpl, err := lookupTemplate(policy.fileType)
	if err != nil {
		return nil, fmt.Errorf("publication planner: %w", err)
	}
	if err := validatePhase5Template(tmpl); err != nil {
		return nil, fmt.Errorf("publication planner: validate template: %w", err)
	}

	required := append([]StepType(nil), tmpl.AllRequired...)
	optional := tmpl.enabledSteps(effective)
	// Ensure media_visible and scrape are always present
	if !containsStepType(optional, StepMediaVisible) {
		optional = append(optional, StepMediaVisible)
	}
	if !containsStepType(optional, StepScrape) {
		optional = append(optional, StepScrape)
	}
	encrypt := p.options.EncryptGlobal && policy.libraryEncrypt
	if encrypt {
		if p.options.EncryptionValidator != nil {
			if err := p.options.EncryptionValidator.ValidateEncryptedLibrary(ctx, tx, EncryptedLibrary{ID: policy.libraryID}); err != nil {
				return nil, fmt.Errorf("publication planner: encrypted library validation: %w", err)
			}
		}
		required = append(required, StepEncrypt)
	}
	prepareEnabled := p.options.PreparePlanner != nil && p.options.Capabilities != nil && p.options.Capabilities.Available(string(StepPrepare)) && policy.jitPrepare && policy.fileType == "video"
	if prepareEnabled {
		optional = append(optional, StepPrepare)
	}
	steps := append(append([]StepType(nil), required...), optional...)
	generation := policy.generation + 1
	requiredSet := map[StepType]bool{}
	for _, step := range required {
		requiredSet[step] = true
	}
	nodes := make([]PlanNode, 0, len(steps))
	for _, step := range steps {
		nodes = append(nodes, PlanNode{Step: step, Generation: generation, Required: requiredSet[step]})
	}
	edges := make([]Dependency, 0, len(optional)+4)
	addEdge := func(step, target StepType, kind DependencyKind) {
		copy := target
		edges = append(edges, Dependency{Step: step, Kind: kind, DependsOn: &copy, Generation: generation, DependsOnGeneration: generation})
	}
	for _, step := range optional {
		if step != StepMediaVisible {
			addEdge(step, StepMediaVisible, DependencySuccess)
		}
	}
	if encrypt {
		addEdge(StepEncrypt, required[0], DependencySuccess)
	}
	if policy.fileType == "video" && effective.SubtitleRecognize {
		addEdge(StepSubtitleRecognize, StepSubtitleExtract, DependencySuccess)
		addEdge(StepSubtitleRecognize, StepAtrackExtract, DependencySuccess)
	}
	// Phase 5 AI edges for all media types
	category := ClassifyMediaType(policy.fileType)
	aiEdges := phase5AIEdges(effective, category)
	for _, aiEdge := range aiEdges {
		addEdge(aiEdge.Step, *aiEdge.DependsOn, aiEdge.Kind)
	}
	// Document-specific edges
	if category == MediaCategoryDocument {
		if effective.ImageOCR {
			addEdge(StepImageOCR, StepDocumentFulltext, DependencySuccess)
		}
		if effective.DocumentFulltext && !effective.ImageOCR {
			addEdge(StepDocumentFulltext, StepDocumentConvert, DependencySuccess)
		}
		if effective.DocumentConvert && !effective.DocumentFulltext {
			addEdge(StepDocumentConvert, StepDocumentFulltext, DependencySuccess)
		}
	}
	graph := PlanGraph{Nodes: nodes, Edges: edges}
	if err := ValidatePlanGraph(graph); err != nil {
		return nil, fmt.Errorf("publication planner: validate graph: %w", err)
	}
	basis := EncryptionCleanupBasis{Encryption: encrypt && policy.cleanupEncrypted, Package: policy.cleanupPackage}
	basis.CleanupEligible = basis.Encryption || basis.Package
	strategies, err := ValidateEncryptedSourceContracts(steps, basis.CleanupEligible, p.options.EncryptedSourceStrategies)
	if err != nil {
		return nil, fmt.Errorf("publication planner: %w", err)
	}
	snapshot := ConfigSnapshot{PolicyVersion: CurrentPolicyVersion, LibraryID: policy.libraryID, FileType: policy.fileType, ProcessingExplicit: policy.explicit, ProcessingEffective: effective, ProcessingProvenance: provenance, LegacyOptionDefaults: legacyDefaults, EncryptedSourceStrategies: strategies, CleanupBasis: basis, PreviewExtract: policy.explicit.Preview, SubtitleAuto: policy.explicit.SubtitleExtract, ATrackAuto: policy.explicit.ATrackExtract, Encrypt: encrypt, Prepare: prepareEnabled, Steps: append([]StepType(nil), steps...), Metadata: metadata, RequiredSteps: append([]StepType(nil), required...), OptionalSteps: append([]StepType(nil), optional...), Dependencies: append([]Dependency(nil), edges...), Graph: graph}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("publication planner: encode snapshot: %w", err)
	}
	return &currentPlan{mediaID: mediaID, scanTaskID: scanTaskID, policy: policy, reason: reason, preserve: preserve, metadata: metadata, required: required, optional: optional, steps: steps, dependencies: edges, graph: graph, snapshotJSON: snapshotJSON}, nil
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

	result, err = tx.ExecContext(ctx, `INSERT INTO media_ingest_run(media_id,generation,scan_task_id,ingest_item_id,reason,status,preserve_visibility,config_snapshot_json,policy_version)
VALUES(?,?,?,?,?,?, ?,?,?)`, plan.mediaID, generation, nullScanTask(plan.scanTaskID), nullIngestItem(plan.ingestItemID), string(plan.reason), "processing", boolDB(plan.preserve), string(plan.snapshotJSON), CurrentPolicyVersion)
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
		taskType := executionTaskType(step)
		maxAttempts := DefaultMaxAttempts(string(taskType))
		result, err = tx.ExecContext(ctx, `INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,node_key,required,status,max_attempts) VALUES(?,?,?,?,?,?,'waiting',?)`, runID, plan.mediaID, generation, step, nodeKeyForStep(step), requiredFlag, maxAttempts)
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
		sc := SourceClassFromReason(plan.reason, plan.ingestItemID)
		bp := sc.BasePriority()
		profile := ResourceProfile{PolicyVersion: CurrentPolicyVersion, LibraryID: plan.policy.libraryID}
		profileJSON, err := json.Marshal(profile)
		if err != nil {
			return Run{}, fmt.Errorf("publication planner: encode resource profile: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO post_ingest_task(media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,task_type,status,max_attempts,source_class,base_priority,library_id,resource_profile_version,resource_profile_json) VALUES(?,?,?,?,?,?,'waiting',?,?,?,?,?,?)`, plan.mediaID, nullScanTask(plan.scanTaskID), runID, stepID, generation, taskType, maxAttempts, int(sc), bp, plan.policy.libraryID, CurrentPolicyVersion, string(profileJSON))
		if err != nil {
			return Run{}, fmt.Errorf("publication planner: enqueue %s step: %w", step, err)
		}
		if err = taskalign.EnsureDomainWaiting(ctx, tx, string(taskType), plan.mediaID); err != nil {
			return Run{}, fmt.Errorf("publication planner: initialize %s domain task: %w", step, err)
		}
	}
	if err := insertDependenciesTx(ctx, tx, plan.dependencies, stepIDs, plan.mediaID, generation, runID); err != nil {
		return Run{}, fmt.Errorf("publication planner: %w", err)
	}
	return Run{ID: runID, MediaID: plan.mediaID, ScanTaskID: plan.scanTaskID, IngestItemID: plan.ingestItemID, LibraryID: plan.policy.libraryID,
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
	rows, err := tx.QueryContext(ctx, `SELECT d.step_id,d.depends_on_step_id FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id WHERE s.run_id=? AND d.dependency_kind IN ('success','terminal') AND d.depends_on_step_id IS NOT NULL`, runID)
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
func nullIngestItem(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func queueBacked(step StepType) bool {
	switch step {
	case StepPoster, StepThumbnail, StepPreview, StepKeyframe, StepSubtitle, StepAtrack, StepEncrypt, StepSubtitleExtract, StepAtrackExtract, StepSubtitleRecognize, StepKeyframeExtract, StepAIAnalysis,
		StepLyricRecognize, StepAudioAnalysis, StepPhotoClassify, StepPhotoGeocode, StepPhotoFace, StepImageOCR, StepDocumentConvert, StepDocumentFulltext:
		return true
	default:
		return false
	}
}

func executionTaskType(step StepType) StepType {
	switch step {
	case StepSubtitleExtract:
		return StepSubtitle
	case StepAtrackExtract:
		return StepAtrack
	case StepKeyframeExtract:
		return StepKeyframe
	default:
		return step
	}
}

func hasExecutableAdapter(registry ExecutableAdapterRegistry, step StepType) bool {
	if registry == nil {
		return false
	}
	adapter, ok := registry.Adapter(step)
	return ok && adapter != nil && adapter.TaskType() == step
}

func stepPtr(step StepType) *StepType { return &step }

func containsStepType(steps []StepType, target StepType) bool {
	for _, s := range steps {
		if s == target {
			return true
		}
	}
	return false
}

func removeStep(steps []StepType, target StepType) []StepType {
	out := make([]StepType, 0, len(steps))
	for _, s := range steps {
		if s != target {
			out = append(out, s)
		}
	}
	return out
}
