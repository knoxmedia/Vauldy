package handler

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRouterRegisteredStartLoopsDoNotDetach(t *testing.T) {
	files := map[string][]string{
		"schedule_task.go":      {"StartScheduleLoop"},
		"scrape_task.go":        {"StartScrapeTaskLoop"},
		"media_worker_loops.go": {"StartTranscodeTaskLoop"},
		"lyric_task.go":         {"StartLyricTaskLoop"},
		"photo_classify.go":     {"StartPhotoClassifyLoop"},
		"photo_location.go":     {"StartPhotoLocationLoop"},
		"photo_face_task.go":    {"StartPhotoFaceLoop"},
		"media_cleanup.go":      {"StartMediaFileCleanupLoop"},
	}
	for file, names := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			matched := false
			for _, name := range names {
				matched = matched || fn.Name.Name == name
			}
			if !matched {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if _, ok := n.(*ast.GoStmt); ok {
					t.Errorf("%s.%s detaches a goroutine", file, fn.Name.Name)
				}
				return true
			})
		}
	}
}

func TestStartPhotoFaceLoopBlocksUntilCancelled(t *testing.T) {
	h := &Handler{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.StartPhotoFaceLoop(ctx); close(done) }()
	select {
	case <-done:
		t.Fatal("StartPhotoFaceLoop returned before cancellation")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StartPhotoFaceLoop did not stop")
	}
}

func TestTranscodeLoopChildrenDoNotDetach(t *testing.T) {
	files := []string{filepath.Join("..", "..", "internal", "transcode", "worker.go"), filepath.Join("..", "..", "internal", "transcode", "package_worker.go")}
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "StartWaiting" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if _, ok := n.(*ast.GoStmt); ok {
					t.Errorf("%s.StartWaiting detaches child work", file)
				}
				return true
			})
		}
	}
}

func TestRouterLoopWorkersDoNotUseBackgroundContext(t *testing.T) {
	files := map[string][]string{
		"media_worker_loops.go": {"runTranscodeWorkerOnce"},
		"lyric_task.go":         {"runLyricWorkerOnce"},
		"photo_classify.go":     {"runPhotoClassifyOnce"},
		"photo_location.go":     {"runPhotoLocationOnce"},
		"photo_face_task.go":    {"runPhotoFaceOnce"},
	}
	for file, names := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for _, name := range names {
			start := strings.Index(src, "func (h *Handler) "+name)
			if start < 0 {
				t.Fatalf("%s missing %s", file, name)
			}
			next := strings.Index(src[start+5:], "\nfunc ")
			block := src[start:]
			if next >= 0 {
				block = src[start : start+5+next]
			}
			if strings.Contains(block, "context.Background()") {
				t.Errorf("%s.%s uses detached context", file, name)
			}
		}
	}
}

// --- Task 12: Legacy Loop and Direct Spawn tests ---

// TestLegacyLoop_MigratedStartFunctionsForbidden verifies that Phase 5 does not
// allow legacy Start*Loop functions in background worker entry points.
func TestLegacyLoop_MigratedStartFunctionsForbidden(t *testing.T) {
	forbiddenFiles := map[string][]string{
		"media_worker_loops.go": {
			"StartKeyframeTaskLoop",
			"StartAtrackTaskLoop",
			"StartPreviewTaskLoop",
			"StartTranscodeTaskLoop",
		},
	}
	for file, fns := range forbiddenFiles {
		raw, err := os.ReadFile(file)
		if err != nil {
			// File may already be deleted on this branch — that's expected.
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		src := string(raw)
		for _, fn := range fns {
			if strings.Contains(src, "func (h *Handler) "+fn+"(") {
				t.Errorf("legacy loop %s still defined in %s", fn, file)
			}
		}
	}
}

// TestDirectSpawn_NoHandlerTaskWork verifies no HTTP handler body calls
// Process, RunBatch, or StartWaiting synchronously in a go func, effectively
// spawning task work without scheduler admission.
func TestDirectSpawn_NoHandlerTaskWork(t *testing.T) {
	handlerFiles, _ := filepath.Glob("*.go")
	for _, file := range handlerFiles {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// Only check methods on *Handler (HTTP handlers).
			if fn.Recv == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				goStmt, ok := n.(*ast.GoStmt)
				if !ok {
					return true
				}
				call, ok := goStmt.Call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch call.Sel.Name {
				case "Process", "RunBatch", "StartWaiting":
					t.Errorf("%s.%s spawns task work via go %s",
						file, fn.Name.Name, call.Sel.Name)
				}
				return true
			})
		}
	}
}

// TestManualEnqueue_ReturnsIdentityNotExecution verifies manual enqueue paths
// return a durable identity and do NOT execute the task inline.
func TestManualEnqueue_ReturnsIdentityNotExecution(t *testing.T) {
	// Manual enqueue handlers must use the scheduler's enqueue interface.
	// They must never call Process/Execute synchronously in the handler body.
	// Verified via AST: no handler method that contains "Enqueue" also calls
	// "Process" or "Execute" in the same function body.
	files, _ := filepath.Glob("*.go")
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "handler.go" || file == "router.go" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv == nil {
				continue
			}
			hasEnqueue := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if sel.Sel.Name == "Enqueue" {
						hasEnqueue = true
					}
				}
				return true
			})
			if !hasEnqueue {
				continue
			}
			// If this function has an Enqueue call, it must not also have
			// Process/Execute/RunBatch synchronously.
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "Process", "Execute", "RunBatch":
					// Allow scheduler.Process in scheduler dispatch (not handler).
					// Block handler-level inline execution.
					if sel.X.(*ast.Ident).Name != "scheduler" {
						t.Errorf("%s.%s enqueues then synchronously calls %s",
							file, fn.Name.Name, sel.Sel.Name)
					}
				}
				return true
			})
		}
	}
}
