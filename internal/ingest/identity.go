package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"knox-media/internal/discovery"
)

var (
	ErrOutsideLibraryRoots = errors.New("path is outside configured library roots")
	ErrUnstableSource      = errors.New("source changed while fingerprinting")
	ErrNonRegularSource    = errors.New("source is not a regular file")
)

type ErrorClass uint8

const (
	ErrorPermanent ErrorClass = iota
	ErrorRetryable
)

type identityError struct {
	class ErrorClass
	op    string
	path  string
	err   error
}

func (e *identityError) Error() string   { return fmt.Sprintf("%s %q: %v", e.op, e.path, e.err) }
func (e *identityError) Unwrap() error   { return e.err }
func (e *identityError) Retryable() bool { return e.class == ErrorRetryable }

func IsRetryable(err error) bool { return ClassifyIdentityError(err) == ErrorRetryable }

func ClassifyIdentityError(err error) ErrorClass {
	if err == nil {
		return ErrorPermanent
	}
	var classified *identityError
	if errors.As(err, &classified) {
		return classified.class
	}
	var retryable interface{ Retryable() bool }
	if errors.As(err, &retryable) && retryable.Retryable() {
		return ErrorRetryable
	}
	if errors.Is(err, ErrUnstableSource) || errors.Is(err, os.ErrNotExist) || discovery.IsRetryablePathError(err) {
		return ErrorRetryable
	}
	return ErrorPermanent
}

func wrapIdentityError(op, path string, err error) error {
	if err == nil {
		return nil
	}
	class := ErrorPermanent
	if errors.Is(err, ErrUnstableSource) || errors.Is(err, os.ErrNotExist) || discovery.IsRetryablePathError(err) {
		class = ErrorRetryable
	}
	return &identityError{class: class, op: op, path: path, err: err}
}

type ResolvedFilesystemPath struct {
	Path        string
	PathKey     string
	Root        string
	Fingerprint Fingerprint
}

type identityHooks struct {
	read                  func(*os.File, []byte) (int, error)
	beforeFinalValidation func() error
}

func (h identityHooks) readFile(f *os.File, p []byte) (int, error) {
	if h.read != nil {
		return h.read(f, p)
	}
	return f.Read(p)
}

// contextFileReader observes cancellation before and after each Read. It cannot
// interrupt a Read already blocked inside the kernel; cancellation is observed
// as soon as that syscall returns.
type contextFileReader struct {
	ctx  context.Context
	f    *os.File
	read func(*os.File, []byte) (int, error)
}

func (r contextFileReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.read(r.f, p)
	if err == nil {
		if contextErr := r.ctx.Err(); contextErr != nil {
			return n, contextErr
		}
	}
	return n, err
}

// NormalizeFilesystemCandidate performs lexical normalization only. PathKey is
// a best-effort index key, never authorization or file/content identity proof.
func NormalizeFilesystemCandidate(libraryID int64, path string) (Candidate, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Candidate{}, wrapIdentityError("normalize", path, err)
	}
	abs = filepath.Clean(abs)
	key, err := discovery.PathKey(abs)
	if err != nil {
		return Candidate{}, wrapIdentityError("normalize", path, err)
	}
	return Candidate{Source: SourceFilesystemEvent, LibraryID: libraryID, Path: abs, PathKey: key}, nil
}

func ResolveFilesystemPath(path string, roots []string) (ResolvedFilesystemPath, error) {
	return ResolveFilesystemPathContext(context.Background(), path, roots)
}

// ResolveFilesystemPathContext opens the candidate once, validates the final
// target of that handle, hashes that same handle, then revalidates both handle
// identity and pathname/root binding before accepting the evidence.
func ResolveFilesystemPathContext(ctx context.Context, path string, roots []string) (ResolvedFilesystemPath, error) {
	return resolveFilesystemPathContextWithHooks(ctx, path, roots, identityHooks{})
}

func resolveFilesystemPathContextWithHooks(ctx context.Context, path string, roots []string, hooks identityHooks) (ResolvedFilesystemPath, error) {
	if err := ctx.Err(); err != nil {
		return ResolvedFilesystemPath{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ResolvedFilesystemPath{}, wrapIdentityError("resolve", path, err)
	}
	abs = filepath.Clean(abs)
	root, ok, err := longestOriginalRoot(abs, roots)
	if err != nil {
		return ResolvedFilesystemPath{}, wrapIdentityError("select root", abs, err)
	}
	if !ok {
		return ResolvedFilesystemPath{}, wrapIdentityError("select root", abs, ErrOutsideLibraryRoots)
	}

	rootFile, err := discovery.OpenFile(root)
	if err != nil {
		return ResolvedFilesystemPath{}, wrapIdentityError("open root", root, err)
	}
	defer rootFile.Close()
	resolvedRoot, err := discovery.ResolveOpenFile(rootFile, root)
	if err != nil {
		return ResolvedFilesystemPath{}, wrapIdentityError("resolve root handle", root, err)
	}

	f, err := discovery.OpenFile(abs)
	if err != nil {
		return ResolvedFilesystemPath{}, wrapIdentityError("open source", abs, err)
	}
	defer f.Close()
	resolvedPath, err := discovery.ResolveOpenFile(f, abs)
	if err != nil {
		return ResolvedFilesystemPath{}, wrapIdentityError("resolve source handle", abs, err)
	}
	if !discovery.PathWithinRoot(resolvedPath, resolvedRoot) {
		return ResolvedFilesystemPath{}, wrapIdentityError("validate containment", abs, ErrOutsideLibraryRoots)
	}

	fp, hashedInfo, err := fingerprintOpenFile(ctx, f, abs, hooks)
	if err != nil {
		return ResolvedFilesystemPath{}, err
	}
	if err := validateFinalBinding(rootFile, root, resolvedRoot, nil, identityHooks{}); err != nil {
		return ResolvedFilesystemPath{}, err
	}
	finalPath, err := discovery.ResolveOpenFile(f, abs)
	if err != nil {
		return ResolvedFilesystemPath{}, wrapIdentityError("revalidate source handle", abs, err)
	}
	finalRoot, err := discovery.ResolveOpenFile(rootFile, root)
	if err != nil {
		return ResolvedFilesystemPath{}, wrapIdentityError("revalidate root handle", root, err)
	}
	if !discovery.PathWithinRoot(finalPath, finalRoot) {
		return ResolvedFilesystemPath{}, wrapIdentityError("revalidate containment", abs, ErrOutsideLibraryRoots)
	}

	key, err := discovery.PathKey(abs)
	if err != nil {
		return ResolvedFilesystemPath{}, wrapIdentityError("path key", abs, err)
	}
	result := ResolvedFilesystemPath{Path: abs, PathKey: key, Root: root, Fingerprint: fp}
	// This validation, including its final f.Stat metadata comparison, must be
	// the last operation before accepting the hash.
	if err := validateFinalBinding(f, abs, resolvedPath, hashedInfo, hooks); err != nil {
		return ResolvedFilesystemPath{}, err
	}
	return result, nil
}

func longestOriginalRoot(path string, roots []string) (string, bool, error) {
	best := ""
	bestKeyLen := -1
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", false, err
		}
		abs = filepath.Clean(abs)
		if !discovery.PathWithinRoot(path, abs) {
			continue
		}
		key, err := discovery.PathKey(abs)
		if err != nil {
			return "", false, err
		}
		if len(key) > bestKeyLen {
			best, bestKeyLen = abs, len(key)
		}
	}
	return best, bestKeyLen >= 0, nil
}

func FingerprintFile(path string) (Fingerprint, error) {
	return FingerprintFileContext(context.Background(), path)
}

func FingerprintFileContext(ctx context.Context, path string) (Fingerprint, error) {
	return fingerprintFileContextWithHooks(ctx, path, identityHooks{})
}

func fingerprintFileContextWithHooks(ctx context.Context, path string, hooks identityHooks) (Fingerprint, error) {
	if err := ctx.Err(); err != nil {
		return Fingerprint{}, err
	}
	f, err := discovery.OpenFile(path)
	if err != nil {
		return Fingerprint{}, wrapIdentityError("open source", path, err)
	}
	defer f.Close()
	resolved, err := discovery.ResolveOpenFile(f, path)
	if err != nil {
		return Fingerprint{}, wrapIdentityError("resolve source handle", path, err)
	}
	fp, hashedInfo, err := fingerprintOpenFile(ctx, f, path, hooks)
	if err != nil {
		return Fingerprint{}, err
	}
	if err := validateFinalBinding(f, path, resolved, hashedInfo, hooks); err != nil {
		return Fingerprint{}, err
	}
	return fp, nil
}

func fingerprintOpenFile(ctx context.Context, f *os.File, path string, hooks identityHooks) (Fingerprint, os.FileInfo, error) {
	before, err := f.Stat()
	if err != nil {
		return Fingerprint{}, nil, wrapIdentityError("stat source handle", path, err)
	}
	if !before.Mode().IsRegular() {
		return Fingerprint{}, nil, wrapIdentityError("validate source type", path, ErrNonRegularSource)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Fingerprint{}, nil, wrapIdentityError("seek source", path, err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, contextFileReader{ctx: ctx, f: f, read: hooks.readFile}); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Fingerprint{}, nil, err
		}
		return Fingerprint{}, nil, wrapIdentityError("hash source", path, err)
	}
	after, err := f.Stat()
	if err != nil {
		return Fingerprint{}, nil, wrapIdentityError("restat source handle", path, err)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime().UnixNano() != after.ModTime().UnixNano() {
		return Fingerprint{}, nil, wrapIdentityError("verify source stability", path, ErrUnstableSource)
	}
	return Fingerprint{SHA256: hex.EncodeToString(h.Sum(nil)), Size: after.Size(), ModTimeNS: after.ModTime().UnixNano()}, after, nil
}

func validateFinalBinding(f *os.File, path, resolvedBefore string, hashedInfo os.FileInfo, hooks identityHooks) error {
	if hooks.beforeFinalValidation != nil {
		if err := hooks.beforeFinalValidation(); err != nil {
			return wrapIdentityError("test final validation seam", path, err)
		}
	}
	pathInfo, err := os.Stat(path)
	if err != nil {
		return wrapIdentityError("revalidate source path", path, err)
	}
	handleInfo, err := f.Stat()
	if err != nil {
		return wrapIdentityError("revalidate source handle", path, err)
	}
	if !os.SameFile(handleInfo, pathInfo) {
		return wrapIdentityError("revalidate source binding", path, ErrUnstableSource)
	}
	resolvedAfter, err := discovery.ResolveOpenFile(f, path)
	if err != nil {
		return wrapIdentityError("revalidate source target", path, err)
	}
	beforeKey, err := discovery.PathKey(resolvedBefore)
	if err != nil {
		return wrapIdentityError("key resolved source", path, err)
	}
	afterKey, err := discovery.PathKey(resolvedAfter)
	if err != nil {
		return wrapIdentityError("key revalidated source", path, err)
	}
	if beforeKey != afterKey {
		return wrapIdentityError("revalidate source target", path, ErrUnstableSource)
	}
	// Keep this metadata check last: the accepted hash is evidence for hashedInfo,
	// and no mutation of the same object may occur after hashing but before return.
	if hashedInfo != nil {
		finalInfo, statErr := f.Stat()
		if statErr != nil {
			return wrapIdentityError("final stat source handle", path, statErr)
		}
		if !os.SameFile(hashedInfo, finalInfo) || hashedInfo.Size() != finalInfo.Size() || hashedInfo.ModTime().UnixNano() != finalInfo.ModTime().UnixNano() {
			return wrapIdentityError("final source metadata", path, ErrUnstableSource)
		}
	}
	return nil
}
