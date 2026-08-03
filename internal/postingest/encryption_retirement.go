package postingest

import (
	"context"
	"fmt"

	"knox-media/internal/publication"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

func encryptionUsesRetirementHandoff(policyVersion int) bool {
	return policyVersion >= publication.PolicyV3
}

var errEncryptionRetirementConflict = storage.ErrEncryptionRetirementConflict

// upsertEncryptionRetirementIntentTx creates or refreshes the generation-scoped
// plaintext retirement row for an encryption basis (Task 11 contract via storage helper).
func upsertEncryptionRetirementIntentTx(ctx context.Context, tx store.SQLExecutor, task Task, s storage.StagedMediaEncryption, quarantinePath string) error {
	if task.RunID == nil || *task.RunID <= 0 || task.Generation <= 0 || task.MediaID <= 0 || task.ID <= 0 {
		return fmt.Errorf("encryption retirement: invalid task identity")
	}
	return storage.UpsertEncryptionRetirementIntentTx(ctx, tx, storage.EncryptionRetirementIntent{
		MediaID:           task.MediaID,
		RunID:             *task.RunID,
		Generation:        task.Generation,
		BasisID:           task.ID,
		StageID:           s.StageID,
		SourcePath:        s.OriginalPath,
		SourceFingerprint: s.SourceFingerprint,
		RetryRound:        task.RetryRound,
		QuarantinePath:    quarantinePath,
		Cleanup:           s.CleanupPlaintext,
	})
}
