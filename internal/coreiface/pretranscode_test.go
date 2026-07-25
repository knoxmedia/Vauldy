package coreiface

import (
	"context"
	"database/sql"
	"testing"

	"knox-media/internal/store"
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

func (testPreparePlanner) PlanIngestPrepareTx(context.Context, store.SQLExecutor, int64, int64, int64, int64) error {
	return nil
}

func TestIngestPreparePlannerAcceptsNonSQLTxExecutor(t *testing.T) {
	var planner IngestPreparePlanner = testPreparePlanner{}
	var executor store.SQLExecutor = minimalSQLExecutor{}
	if err := planner.PlanIngestPrepareTx(context.Background(), executor, 1, 2, 3, 4); err != nil {
		t.Fatal(err)
	}
}

type minimalSQLExecutor struct{}

func (minimalSQLExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, nil
}
func (minimalSQLExecutor) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}
func (minimalSQLExecutor) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }
