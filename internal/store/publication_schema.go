package store

import "context"

// ValidatePublicationV2Schema verifies the exact migrated publication graph.
func ValidatePublicationV2Schema(ctx context.Context, q SQLExecutor) error {
	return validatePublicationV2Schema(ctx, q)
}
