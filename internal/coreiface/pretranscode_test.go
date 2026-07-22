package coreiface

import (
	"context"
	"database/sql"
	"testing"
)

func TestCommunityRegistryHasNoInjectedIngestPreparePlanner(t *testing.T) {
	if IngestPreparePlan != nil {
		t.Fatal("coreiface unexpectedly injected enterprise prepare planner")
	}
}

func TestRegisterIngestPreparePlannerRestoresPreviousHandle(t *testing.T) {
	original := IngestPreparePlannerHandle()
	planner := testPreparePlanner{}
	restore := RegisterIngestPreparePlanner(planner)
	if IngestPreparePlannerHandle() == nil {
		t.Fatal("planner not registered")
	}
	restore()
	if IngestPreparePlannerHandle() != original {
		t.Fatal("planner handle not restored")
	}
}

type testPreparePlanner struct{}

func (testPreparePlanner) PlanIngestPrepareTx(context.Context, *sql.Tx, int64, int64, int64, int64) error {
	return nil
}
