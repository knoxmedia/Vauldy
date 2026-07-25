package imagethumb

import "context"

type commitGuardKey struct{}
type CommitGuard func(context.Context) error

// WithCommitGuard fences publication of generated photo variants.
func WithCommitGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	if guard == nil {
		return ctx
	}
	return context.WithValue(ctx, commitGuardKey{}, CommitGuard(guard))
}

func runCommitGuard(ctx context.Context) error {
	guard, _ := ctx.Value(commitGuardKey{}).(CommitGuard)
	if guard == nil {
		return nil
	}
	return guard(ctx)
}
