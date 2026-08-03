package publication

import "knox-media/internal/store"

func init() {
	store.ResetInterruptedScrapeFn = ResetInterruptedScrapeTasks
}
