package doccover

import (
	"os"
	"strings"
	"testing"
)

func TestWorkerLifecycleUsesParentContext(t *testing.T) {
	raw, err := os.ReadFile("worker.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, "w.RunOnceContext(ctx, id)") {
		t.Fatal("Start does not pass server context to jobs")
	}
	start := strings.Index(src, "func (w *Worker) RunOnceContext")
	if start < 0 {
		t.Fatal("RunOnceContext missing")
	}
	block := src[start:]
	if strings.Contains(block, "context.WithTimeout(context.Background()") {
		t.Fatal("RunOnceContext derives timeout from Background")
	}
	if !strings.Contains(block, "context.WithTimeout(ctx,") {
		t.Fatal("RunOnceContext does not derive timeout from parent")
	}
}
