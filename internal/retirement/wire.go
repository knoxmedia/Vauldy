package retirement

import "knox-media/internal/publication"

// init registers the retirement barrier recompute with publication lifecycle finalization.
func init() {
	publication.SetRetirementBarrierRecompute(RecomputeRetirementBarrierTx)
}
