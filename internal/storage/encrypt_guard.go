package storage

import (
	"context"
	"database/sql"
)

type encryptCommitGuardKey struct{}
type encryptCommitGuardTxKey struct{}

// WithEncryptCommitGuard fences database publication after encryption work.
func WithEncryptCommitGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	if guard == nil {
		return ctx
	}
	return context.WithValue(ctx, encryptCommitGuardKey{}, guard)
}

// WithEncryptCommitGuardTx makes the ownership check and encryption publication atomic.
func WithEncryptCommitGuardTx(ctx context.Context, guard func(context.Context, *sql.Tx) error) context.Context {
	if guard == nil {
		return ctx
	}
	return context.WithValue(ctx, encryptCommitGuardTxKey{}, guard)
}

func runEncryptCommitGuard(ctx context.Context, tx *sql.Tx) error {
	if guard, _ := ctx.Value(encryptCommitGuardTxKey{}).(func(context.Context, *sql.Tx) error); guard != nil {
		return guard(ctx, tx)
	}
	if guard, _ := ctx.Value(encryptCommitGuardKey{}).(func(context.Context) error); guard != nil {
		return guard(ctx)
	}
	return nil
}
