package playcompletion

// Event identifies the kind of playback evidence received from a player.
type Event string

const (
	EventStart    Event = "start"
	EventProgress Event = "progress"
	EventSeek     Event = "seek"
	EventEnded    Event = "ended"
)

const (
	MinValidPlaySeconds      = 60.0
	MaxEvidenceGapMS         = int64(30_000)
	PositionToleranceSeconds = int64(5)
	MaxPlaybackRate          = 3.0
)

// State is the persisted natural-play evidence for one playback session.
type State struct {
	LastPosition     int64
	LastReceivedAtMS int64
	LastSequence     int64
	ValidPlaySeconds float64
	AwaitingBaseline bool
}

// Input contains authoritative media data and one playback event.
type Input struct {
	FileType            string
	Duration            int64
	Position            int64
	Sequence            int64
	ReceivedAtMS        int64
	Event               Event
	PreviouslyCompleted bool
}

// Result contains the next evidence state and completion decision.
type Result struct {
	State          State
	Completed      bool
	AutoCompleted  bool
	AcceptPosition bool
	Stale          bool
}

// CompletionPosition returns the first whole second at or above the
// duration-dependent completion percentage. A non-positive duration is unknown.
func CompletionPosition(duration int64) int64 {
	if duration <= 0 {
		return 0
	}

	percentage := int64(95)
	if duration > 3600 {
		percentage = 90
	} else if duration > 1200 {
		percentage = 93
	}

	// Split before multiplying to avoid overflowing for large int64 durations.
	wholeHundreds, remainder := duration/100, duration%100
	return wholeHundreds*percentage + (remainder*percentage+99)/100
}

// Evaluate deterministically applies one playback event to session evidence.
func Evaluate(state State, input Input) Result {
	completed := input.PreviouslyCompleted
	if !validEvent(input.Event) {
		return Result{State: state, Completed: completed}
	}
	if input.Sequence <= state.LastSequence {
		return Result{State: state, Completed: completed, Stale: true}
	}

	position := acceptedPosition(input.Position, input.Duration)
	next := state
	next.LastPosition = position
	next.LastReceivedAtMS = input.ReceivedAtMS
	next.LastSequence = input.Sequence
	naturalProgress := false

	switch input.Event {
	case EventStart, EventSeek:
		next.AwaitingBaseline = true
	case EventProgress:
		if !state.AwaitingBaseline {
			added := nextEvidenceSeconds(state, position, input.ReceivedAtMS)
			next.ValidPlaySeconds += added
			naturalProgress = added > 0
		}
		next.AwaitingBaseline = false
	case EventEnded:
		completed = true
	}

	autoCompleted := false
	if !completed && naturalProgress && input.FileType == "video" && input.Duration > 0 &&
		next.ValidPlaySeconds >= MinValidPlaySeconds && position >= CompletionPosition(input.Duration) {
		completed = true
		autoCompleted = true
	}

	return Result{
		State:          next,
		Completed:      completed,
		AutoCompleted:  autoCompleted,
		AcceptPosition: true,
	}
}

func validEvent(event Event) bool {
	switch event {
	case EventStart, EventProgress, EventSeek, EventEnded:
		return true
	default:
		return false
	}
}

func acceptedPosition(position, duration int64) int64 {
	if position < 0 {
		return 0
	}
	if duration > 0 && position > duration {
		return duration
	}
	return position
}

func nextEvidenceSeconds(state State, position, receivedAtMS int64) float64 {
	if receivedAtMS <= state.LastReceivedAtMS || position <= state.LastPosition {
		return 0
	}

	// Unsigned subtraction after signed ordering yields the exact positive
	// distance even when the endpoints span the full int64 range.
	elapsedMS := uint64(receivedAtMS) - uint64(state.LastReceivedAtMS)
	if elapsedMS > uint64(MaxEvidenceGapMS) {
		return 0
	}
	positionDelta := uint64(position) - uint64(state.LastPosition)
	elapsedSeconds := float64(elapsedMS) / 1000
	if float64(positionDelta) > elapsedSeconds*MaxPlaybackRate+float64(PositionToleranceSeconds) {
		return 0
	}
	if float64(positionDelta) < elapsedSeconds {
		return float64(positionDelta)
	}
	return elapsedSeconds
}
