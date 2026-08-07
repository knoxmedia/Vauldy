package api

import (
	"os"
	"strings"
	"testing"
)

func TestSharedResourceControlAssemblyRouterDoesNotConstructResources(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, forbidden := range []string{"postingest.NewQueue(", "postingest.NewEnqueuer(", "postingest.NewDispatcher(", "scancoord.New("} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("router constructs resource control dependency %q", forbidden)
		}
	}
	if !strings.Contains(source, "deps handler.Dependencies") || !strings.Contains(source, "handler.New(application, deps)") {
		t.Fatal("NewEngine does not pass explicit handler dependencies")
	}
}
