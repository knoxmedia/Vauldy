package handler

// pretranscodeTaskController is the commercial pretranscode task surface.
// Community builds leave pretranscodeModule() nil.
type pretranscodeTaskController interface {
	RetryTask(id int64) error
}

type pretranscodeHandle struct {
	Task pretranscodeTaskController
}

// pretranscodeModule returns the commercial pretranscode handle when linked.
// Vauldy community builds always return nil.
func pretranscodeModule() *pretranscodeHandle {
	return nil
}
