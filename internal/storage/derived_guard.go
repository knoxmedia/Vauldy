package storage

import (
	"context"
	"database/sql"
)

type derivedCommitGuardTxKey struct{}
type DerivedCommitGuardTx func(context.Context, *sql.Tx) error

// WithDerivedCommitGuardTx fences publication of staged derived artifacts.
func WithDerivedCommitGuardTx(ctx context.Context, guard func(context.Context, *sql.Tx) error) context.Context {
	if guard == nil {
		return ctx
	}
	return context.WithValue(ctx, derivedCommitGuardTxKey{}, DerivedCommitGuardTx(guard))
}

func runDerivedCommitGuardTx(ctx context.Context, tx *sql.Tx) error {
	guard, _ := ctx.Value(derivedCommitGuardTxKey{}).(DerivedCommitGuardTx)
	if guard == nil {
		return nil
	}
	return guard(ctx, tx)
}
