// Package session provides Redis-free JIT transcode session management.
// Each playback session owns a temp directory, an ffmpeg process, and a cancellable context.
package session

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"knox-media/internal/keystore"
)

// JITSegmentDurationSeconds is the segment length used by the JIT ffmpeg segment muxer
// (-segment_time) and by HTTP handlers when mapping segment indices to input seek times.
const JITSegmentDurationSeconds = 3.0

// Manager tracks active JIT playback sessions.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session

	ffmpegPath    string
	ffprobePath   string
	dataDir       string
	keyframesDir  string
	DB            *sql.DB
	Vault         *keystore.Vault
}

// NewManager creates a new session manager and removes any leftover files under
// dataDir/jit from prior runs.
func NewManager(ffmpegPath, ffprobePath, dataDir, keyframesDir string, db *sql.DB, vault *keystore.Vault) (*Manager, error) {
	jitDir := filepath.Join(dataDir, "jit")
	if err := os.RemoveAll(jitDir); err != nil {
		return nil, fmt.Errorf("clear jit temp dir: %w", err)
	}
	m := &Manager{
		sessions:     make(map[string]*Session),
		ffmpegPath:   ffmpegPath,
		ffprobePath:  ffprobePath,
		dataDir:      dataDir,
		keyframesDir: keyframesDir,
		DB:           db,
		Vault:        vault,
	}
	return m, nil
}

// Session represents one JIT playback session.
type Session struct {
	ID           string
	FileID       string
	MediaID      int64
	SourcePath   string
	TempDir      string
	Bitrate      string
	Resolution   string
	StartSeg     int
	Duration     float64 // source video duration in seconds
	latestSeg    atomic.Int64
	requestedSeg atomic.Int64 // last segment requested by player
	ffmpegPath   string
	mgr          *Manager // back-reference for cleanup

	resumeWatchEpoch atomic.Uint32 // invalidates older post-resume stall checks

	CreatedAt       time.Time
	lastAccess      atomic.Value // time.Time
	lastRequestTime atomic.Value // time.Time

	cancel context.CancelFunc
	Cmd    *exec.Cmd
	ctx    context.Context

	StreamEncryption *StreamEncryption

	Mu    sync.Mutex
	runMu sync.Mutex // one runTranscode at a time per session (avoids overlapping ffmpeg)
	done  chan struct{}
	paused atomic.Bool
}

// CreateSession allocates a new JIT playback session.
func (m *Manager) CreateSession(mediaID int64, fileID, sourcePath, bitrate, resolution string, duration float64) (*Session, error) {
	m.mu.Lock()
	id := newSessionID()
	tempDir := filepath.Join(m.dataDir, "jit", id)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ffmpeg := m.ffmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	s := &Session{
		ffmpegPath: ffmpeg,
		mgr:        m,
		ID:         id,
		FileID:     fileID,
		MediaID:    mediaID,
		SourcePath: sourcePath,
		TempDir:    tempDir,
		Bitrate:    bitrate,
		Resolution: resolution,
		Duration:   duration,
		CreatedAt:  time.Now(),
		cancel:     cancel,
		ctx:        ctx,
		done:       make(chan struct{}),
	}
	s.updateLastAccess()
	s.latestSeg.Store(-1)

	m.sessions[id] = s
	m.mu.Unlock()
	go s.schedulerLoop()
	return s, nil
}

// ValidJITSessionID returns true for ids produced by newSessionID (jit-<decimal>).
func ValidJITSessionID(id string) bool {
	const pfx = "jit-"
	if !strings.HasPrefix(id, pfx) {
		return false
	}
	rest := id[len(pfx):]
	if len(rest) == 0 || len(rest) > 32 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return false
		}
	}
	return true
}

// RestoreSession re-registers a session under an existing ID after the in-memory session
// was removed (e.g. idle cleanup) while the client still uses the same JIT URLs.
func (m *Manager) RestoreSession(id string, mediaID int64, fileID, sourcePath, bitrate, resolution string, duration float64) (*Session, error) {
	if !ValidJITSessionID(id) {
		return nil, fmt.Errorf("invalid session id")
	}
	m.mu.Lock()
	if existing := m.sessions[id]; existing != nil {
		m.mu.Unlock()
		return existing, nil
	}
	tempDir := filepath.Join(m.dataDir, "jit", id)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ffmpeg := m.ffmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	s := &Session{
		ffmpegPath: ffmpeg,
		mgr:        m,
		ID:         id,
		FileID:     fileID,
		MediaID:    mediaID,
		SourcePath: sourcePath,
		TempDir:    tempDir,
		Bitrate:    bitrate,
		Resolution: resolution,
		Duration:   duration,
		CreatedAt:  time.Now(),
		cancel:     cancel,
		ctx:        ctx,
		done:       make(chan struct{}),
	}
	s.updateLastAccess()
	s.latestSeg.Store(-1)
	m.sessions[id] = s
	m.mu.Unlock()
	go s.schedulerLoop()
	return s, nil
}

// PrepareSegmentWindow sets StartSeg and latestSeg for an initial transcode at startSeg
// without cancelling a running encoder (use when no ffmpeg is running yet).
func (s *Session) PrepareSegmentWindow(startSeg int) {
	s.StartSeg = startSeg
	s.latestSeg.Store(int64(startSeg - 1))
}

// NextSegmentToEmit returns the segment index the next ffmpeg run should use for
// -segment_start_number. After muxing up to L, the next file must be (L+1).ts even if
// StartSeg is still an older playlist low-water mark (e.g. after throttle pause).
func (s *Session) NextSegmentToEmit() int {
	L := int(s.latestSeg.Load())
	start := s.StartSeg
	if L >= start {
		return L + 1
	}
	return start
}

// Get returns an active session by ID, or nil.
func (m *Manager) Get(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// ActiveSessionCount returns the number of in-memory JIT playback sessions.
func (m *Manager) ActiveSessionCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// HasActiveMedia reports whether any JIT session is using the given media id.
func (m *Manager) HasActiveMedia(mediaID int64) bool {
	if m == nil || mediaID <= 0 {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if s != nil && s.MediaID == mediaID {
			return true
		}
	}
	return false
}

// CancelSession stops the ffmpeg process and removes the temp directory.
func (m *Manager) CancelSession(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	if s != nil {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if s == nil {
		return
	}
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}
	_ = os.RemoveAll(s.TempDir)
}

// PauseSession stops the JIT ffmpeg pipeline (throttle); see Session.pause.
func (m *Manager) PauseSession(id string) {
	s := m.Get(id)
	if s == nil {
		return
	}
	s.pause()
}

// ResumeSession resumes the ffmpeg process.
func (m *Manager) ResumeSession(id string) {
	s := m.Get(id)
	if s == nil {
		return
	}
	s.resume()
}

// Heartbeat updates the last access time.
func (m *Manager) Heartbeat(id string) {
	s := m.Get(id)
	if s != nil {
		s.updateLastAccess()
	}
}

// LatestSegment returns the highest segment number produced by ffmpeg.
func (s *Session) LatestSegment() int {
	return int(s.latestSeg.Load())
}

// SetLatestSeg atomically updates the latest segment number.
func (s *Session) SetLatestSeg(seg int) {
	s.latestSeg.Store(int64(seg))
}

// Ctx returns the session's cancellable context.
func (s *Session) Ctx() context.Context {
	return s.ctx
}

// Done returns a channel that closes when the session's ffmpeg exits.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// SignalDone should be called when ffmpeg exits. Safe to call multiple times.
func (s *Session) SignalDone() {
	select {
	case <-s.done:
		return
	default:
		close(s.done)
	}
}

// BumpResumeWatchEpoch starts a new post-resume stall-watch generation; older arms become stale.
func (s *Session) BumpResumeWatchEpoch() uint32 {
	return s.resumeWatchEpoch.Add(1)
}

// ResumeWatchStale is true if a newer stall-watch was armed after ev was captured.
func (s *Session) ResumeWatchStale(ev uint32) bool {
	return s.resumeWatchEpoch.Load() != ev
}

// SetCmd stores the ffmpeg command for pause/resume control.
func (s *Session) SetCmd(cmd *exec.Cmd) {
	s.Mu.Lock()
	s.Cmd = cmd
	s.Mu.Unlock()
}

// pause stops the ffmpeg pipeline (throttle) without advancing StartSeg.
// Advancing StartSeg to latest+1 broke clients still requesting earlier segment URLs that
// already exist on disk (rollback spam). The next run uses NextSegmentToEmit() for
// -segment_start_number and matching input time from the handler.
func (s *Session) pause() {
	s.BumpResumeWatchEpoch()
	s.Mu.Lock()
	cmd := s.Cmd
	s.Mu.Unlock()
	if cmd == nil {
		return
	}

	s.paused.Store(true)
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}

	s.Mu.Lock()
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.done = make(chan struct{})
	s.Cmd = nil
	s.Mu.Unlock()
}

// resume clears the paused flag and resumes an OS-suspended process if one exists (legacy path).
func (s *Session) resume() {
	s.Mu.Lock()
	cmd := s.Cmd
	s.Mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = resumeProcess(cmd.Process.Pid)
	}
	s.paused.Store(false)
}

func (s *Session) updateLastAccess() {
	s.lastAccess.Store(time.Now())
}

// LastAccess returns when the session was last accessed.
func (s *Session) LastAccess() time.Time {
	v := s.lastAccess.Load()
	if v == nil {
		return time.Time{}
	}
	return v.(time.Time)
}

// RecordRequest records a player segment request.
func (s *Session) RecordRequest(seg int) {
	s.requestedSeg.Store(int64(seg))
	s.lastRequestTime.Store(time.Now())
	s.updateLastAccess()
}

// LastRequestedSeg returns the most recent segment requested by the player.
func (s *Session) LastRequestedSeg() int {
	return int(s.requestedSeg.Load())
}

// TimeSinceLastRequest returns how long since the player last requested a segment.
// When no segment has been served yet, uses CreatedAt so the scheduler does not treat
// a new session as idle for a full hour (which would trip the 120s timeout immediately).
func (s *Session) TimeSinceLastRequest() time.Duration {
	v := s.lastRequestTime.Load()
	if v == nil {
		return time.Since(s.CreatedAt)
	}
	return time.Since(v.(time.Time))
}

// ResetForSeek cancels the current ffmpeg, resets context, and updates StartSeg.
// The temp dir is preserved but encoder outputs from startSeg onward are removed so
// a restarted HLS muxer (append_list) does not conflict with stale playlists/segments.
// Caller must restart transcode with new start time.
func (s *Session) ResetForSeek(startSeg int) {
	s.BumpResumeWatchEpoch()
	// Cancel existing ffmpeg.
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}
	_ = cleanupEncoderOutputFromSeg(s.TempDir, startSeg)
	// Reset context and done channel.
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.done = make(chan struct{})
	s.StartSeg = startSeg
	s.latestSeg.Store(int64(startSeg - 1))
	s.Cmd = nil
	s.paused.Store(false)
}

// cleanupEncoderOutputFromSeg removes ffmpeg segment-list and segment files at or after
// startSeg. Encryption key material (enc.key, enc.keyinfo) is left intact.
func cleanupEncoderOutputFromSeg(tempDir string, startSeg int) error {
	if strings.TrimSpace(tempDir) == "" {
		return nil
	}
	_ = os.Remove(filepath.Join(tempDir, "master.m3u8"))
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".ts") {
			continue
		}
		idStr := strings.TrimSuffix(name, filepath.Ext(name))
		segID, perr := strconv.Atoi(idStr)
		if perr != nil || segID < startSeg {
			continue
		}
		_ = os.Remove(filepath.Join(tempDir, name))
	}
	return nil
}

func newSessionID() string {
	return fmt.Sprintf("jit-%d", time.Now().UnixNano())
}

// resumeProcess is platform-specific (NtResumeProcess / SIGCONT) for legacy OS-suspended ffmpeg.
func resumeProcess(pid int) error {
	return platformResume(pid)
}

// SegInRange checks if target segment is within the current transcode range.
func (s *Session) SegInRange(targetSeg int) bool {
	start := s.StartSeg
	latest := int(s.latestSeg.Load())
	if latest < start {
		latest = start
	}
	return targetSeg >= start && targetSeg <= latest+30
}

// Helper to clean path separators.
var _ = strings.TrimSpace
var _ = filepath.Join
