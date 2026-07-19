package subtitle

import (
	"context"
	"database/sql"
)

type commitGuardKey struct{}
type commitGuardTxKey struct{}
type CommitGuard func(context.Context) error
type CommitGuardTx func(context.Context, *sql.Tx) error

func WithCommitGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	if guard == nil {
		return ctx
	}
	return context.WithValue(ctx, commitGuardKey{}, CommitGuard(guard))
}
func WithCommitGuardTx(ctx context.Context, guard func(context.Context, *sql.Tx) error) context.Context {
	if guard == nil {
		return ctx
	}
	return context.WithValue(ctx, commitGuardTxKey{}, CommitGuardTx(guard))
}
func validateCommitGuard(ctx context.Context) error {
	g, _ := ctx.Value(commitGuardKey{}).(CommitGuard)
	if g == nil {
		return nil
	}
	return g(ctx)
}
func validateCommitGuardTx(ctx context.Context, tx *sql.Tx) error {
	g, _ := ctx.Value(commitGuardTxKey{}).(CommitGuardTx)
	if g != nil {
		return g(ctx, tx)
	}
	return validateCommitGuard(ctx)
}
