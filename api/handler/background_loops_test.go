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
