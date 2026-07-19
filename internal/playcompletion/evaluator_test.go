package playcompletion

import (
	"math"
	"reflect"
	"testing"
)

func TestCompletionPositionUsesDurationBandsAndCeilingArithmetic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		duration int64
		want     int64
	}{
		{name: "unknown negative", duration: -1, want: 0},
		{name: "unknown zero", duration: 0, want: 0},
		{name: "one second rounds up", duration: 1, want: 1},
		{name: "twenty minutes minus one", duration: 1199, want: 1140},
		{name: "twenty minutes exact", duration: 1200, want: 1140},
		{name: "twenty minutes plus one", duration: 1201, want: 1117},
		{name: "sixty minutes minus one", duration: 3599, want: 3348},
		{name: "sixty minutes exact", duration: 3600, want: 3348},
		{name: "sixty minutes plus one", duration: 3601, want: 3241},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompletionPosition(tc.duration); got != tc.want {
				t.Fatalf("CompletionPosition(%d) = %d, want %d", tc.duration, got, tc.want)
			}
		})
	}
}

func TestCompletionPositionNeverFallsBelowConfiguredPercentage(t *testing.T) {
	t.Parallel()

	for duration := int64(1); duration <= 10_000; duration++ {
		percentage := int64(95)
		switch {
		case duration > 3600:
			percentage = 90
		case duration > 1200:
			percentage = 93
		}
		got := CompletionPosition(duration)
		if got*100 < duration*percentage {
			t.Fatalf("duration=%d threshold=%d is below %d%%", duration, got, percentage)
		}
		if got > 0 && (got-1)*100 >= duration*percentage {
			t.Fatalf("duration=%d threshold=%d is not the smallest ceiling at %d%%", duration, got, percentage)
		}
	}
}

func TestEvaluateStartAndSeekResetBaselineWithoutEvidence(t *testing.T) {
	t.Parallel()

	for _, event := range []Event{EventStart, EventSeek} {
		t.Run(string(event), func(t *testing.T) {
			initial := State{LastPosition: 100, LastReceivedAtMS: 1_000, LastSequence: 4, ValidPlaySeconds: 17.5}
			got := Evaluate(initial, Input{FileType: "video", Duration: 1_000, Position: 900, Sequence: 5, ReceivedAtMS: 2_000, Event: event})
			want := State{LastPosition: 900, LastReceivedAtMS: 2_000, LastSequence: 5, ValidPlaySeconds: 17.5, AwaitingBaseline: true}
			assertResultState(t, got, want)
			if !got.AcceptPosition || got.Completed || got.AutoCompleted || got.Stale {
				t.Fatalf("unexpected flags: %+v", got)
			}
		})
	}
}

func TestEvaluateFirstProgressAfterSeekOnlyEstablishesBaseline(t *testing.T) {
	t.Parallel()

	initial := State{LastPosition: 900, LastReceivedAtMS: 1_000, LastSequence: 5, ValidPlaySeconds: 59, AwaitingBaseline: true}
	got := Evaluate(initial, Input{FileType: "video", Duration: 1_000, Position: 950, Sequence: 6, ReceivedAtMS: 11_000, Event: EventProgress})
	want := State{LastPosition: 950, LastReceivedAtMS: 11_000, LastSequence: 6, ValidPlaySeconds: 59, AwaitingBaseline: false}
	assertResultState(t, got, want)
	if got.Completed || got.AutoCompleted {
		t.Fatalf("baseline progress completed unexpectedly: %+v", got)
	}
}

func TestEvaluateBaselineOrRejectedJumpCannotConsumeExistingEvidenceToComplete(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		state State
		input Input
	}{
		{
			name:  "first progress after seek",
			state: State{LastPosition: 100, LastReceivedAtMS: 1_000, LastSequence: 2, ValidPlaySeconds: 60, AwaitingBaseline: true},
			input: Input{FileType: "video", Duration: 1_000, Position: 950, Sequence: 3, ReceivedAtMS: 11_000, Event: EventProgress},
		},
		{
			name:  "implicit jump over tolerance",
			state: State{LastPosition: 100, LastReceivedAtMS: 1_000, LastSequence: 2, ValidPlaySeconds: 60},
			input: Input{FileType: "video", Duration: 1_000, Position: 950, Sequence: 3, ReceivedAtMS: 11_000, Event: EventProgress},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.state, tc.input)
			if got.Completed || got.AutoCompleted {
				t.Fatalf("non-natural threshold arrival completed unexpectedly: %+v", got)
			}
			if got.State.ValidPlaySeconds != tc.state.ValidPlaySeconds {
				t.Fatalf("valid seconds changed: got %v want %v", got.State.ValidPlaySeconds, tc.state.ValidPlaySeconds)
			}
		})
	}
}
func TestEvaluateNaturalProgressAddsRealServerSeconds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		position  int64
		elapsedMS int64
		wantAdded float64
	}{
		{name: "one times", position: 110, elapsedMS: 10_000, wantAdded: 10},
		{name: "two times", position: 120, elapsedMS: 10_000, wantAdded: 10},
		{name: "position delta limits evidence", position: 105, elapsedMS: 10_000, wantAdded: 5},
		{name: "exact jump tolerance accepted", position: 135, elapsedMS: 10_000, wantAdded: 10},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initial := State{LastPosition: 100, LastReceivedAtMS: 1_000, LastSequence: 1, ValidPlaySeconds: 7}
			got := Evaluate(initial, Input{FileType: "video", Duration: 1_000, Position: tc.position, Sequence: 2, ReceivedAtMS: 1_000 + tc.elapsedMS, Event: EventProgress})
			if got.State.ValidPlaySeconds != 7+tc.wantAdded {
				t.Fatalf("valid seconds = %v, want %v", got.State.ValidPlaySeconds, 7+tc.wantAdded)
			}
		})
	}
}

func TestEvaluateInvalidNaturalSegmentsAddNoEvidenceAndBecomeNewBaseline(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		position   int64
		receivedMS int64
	}{
		{name: "zero elapsed", position: 101, receivedMS: 1_000},
		{name: "clock moved backwards", position: 101, receivedMS: 999},
		{name: "gap over thirty seconds", position: 110, receivedMS: 31_001},
		{name: "backwards movement", position: 99, receivedMS: 11_000},
		{name: "no movement", position: 100, receivedMS: 11_000},
		{name: "jump over tolerance", position: 136, receivedMS: 11_000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initial := State{LastPosition: 100, LastReceivedAtMS: 1_000, LastSequence: 1, ValidPlaySeconds: 12.5}
			got := Evaluate(initial, Input{FileType: "video", Duration: 1_000, Position: tc.position, Sequence: 2, ReceivedAtMS: tc.receivedMS, Event: EventProgress})
			if got.State.ValidPlaySeconds != initial.ValidPlaySeconds {
				t.Fatalf("valid seconds changed: got %v want %v", got.State.ValidPlaySeconds, initial.ValidPlaySeconds)
			}
			if got.State.LastPosition != tc.position || got.State.LastReceivedAtMS != tc.receivedMS || got.State.LastSequence != 2 {
				t.Fatalf("invalid segment did not establish baseline: %+v", got.State)
			}
		})
	}
}

func TestEvaluateThirtySecondGapBoundaryIsAccepted(t *testing.T) {
	t.Parallel()

	initial := State{LastPosition: 100, LastReceivedAtMS: 1_000, LastSequence: 1}
	got := Evaluate(initial, Input{FileType: "video", Duration: 1_000, Position: 130, Sequence: 2, ReceivedAtMS: 31_000, Event: EventProgress})
	if got.State.ValidPlaySeconds != 30 {
		t.Fatalf("valid seconds = %v, want 30", got.State.ValidPlaySeconds)
	}
}

func TestEvaluateStaleSequenceLeavesStateByteForByteUnchanged(t *testing.T) {
	t.Parallel()

	initial := State{LastPosition: 333, LastReceivedAtMS: 9_000, LastSequence: 7, ValidPlaySeconds: 22.25, AwaitingBaseline: true}
	for _, sequence := range []int64{6, 7} {
		for _, event := range []Event{EventStart, EventProgress, EventSeek, EventEnded} {
			got := Evaluate(initial, Input{FileType: "video", Duration: 400, Position: 400, Sequence: sequence, ReceivedAtMS: 99_000, Event: event})
			if !got.Stale || got.AcceptPosition || got.AutoCompleted {
				t.Fatalf("sequence=%d event=%q flags=%+v", sequence, event, got)
			}
			if !reflect.DeepEqual(got.State, initial) {
				t.Fatalf("sequence=%d event=%q state changed: got %+v want %+v", sequence, event, got.State, initial)
			}
		}
	}
}

func TestEvaluateAutoCompletionRequiresSixtySecondsAndThresholdPosition(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		initialValid  float64
		position      int64
		wantCompleted bool
	}{
		{name: "sixty at threshold", initialValid: 50, position: 950, wantCompleted: true},
		{name: "fifty nine at threshold", initialValid: 49, position: 950, wantCompleted: false},
		{name: "sixty below threshold", initialValid: 50, position: 949, wantCompleted: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initial := State{LastPosition: tc.position - 10, LastReceivedAtMS: 1_000, LastSequence: 1, ValidPlaySeconds: tc.initialValid}
			got := Evaluate(initial, Input{FileType: "video", Duration: 1_000, Position: tc.position, Sequence: 2, ReceivedAtMS: 11_000, Event: EventProgress})
			if got.Completed != tc.wantCompleted || got.AutoCompleted != tc.wantCompleted {
				t.Fatalf("completed=%v auto=%v, want both %v; result=%+v", got.Completed, got.AutoCompleted, tc.wantCompleted, got)
			}
		})
	}
}

func TestEvaluateDoesNotAutoCompleteNonVideoOrUnknownDuration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		fileType string
		duration int64
	}{
		{name: "audio", fileType: "audio", duration: 1_000},
		{name: "photo", fileType: "photo", duration: 1_000},
		{name: "unknown zero", fileType: "video", duration: 0},
		{name: "unknown negative", fileType: "video", duration: -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initial := State{LastPosition: 990, LastReceivedAtMS: 1_000, LastSequence: 1, ValidPlaySeconds: 100}
			got := Evaluate(initial, Input{FileType: tc.fileType, Duration: tc.duration, Position: 1_000, Sequence: 2, ReceivedAtMS: 11_000, Event: EventProgress})
			if got.Completed || got.AutoCompleted {
				t.Fatalf("completed unexpectedly: %+v", got)
			}
		})
	}
}

func TestEvaluateUnsupportedEventFailsClosed(t *testing.T) {
	t.Parallel()

	initial := State{LastPosition: 42, LastReceivedAtMS: 1_000, LastSequence: 7, ValidPlaySeconds: 59.5, AwaitingBaseline: true}
	for _, previouslyCompleted := range []bool{false, true} {
		got := Evaluate(initial, Input{
			FileType:            "video",
			Duration:            100,
			Position:            99,
			Sequence:            8,
			ReceivedAtMS:        2_000,
			Event:               Event("unsupported"),
			PreviouslyCompleted: previouslyCompleted,
		})
		if !reflect.DeepEqual(got.State, initial) {
			t.Fatalf("previouslyCompleted=%v state changed: got %+v want %+v", previouslyCompleted, got.State, initial)
		}
		if got.AcceptPosition || got.AutoCompleted || got.Stale || got.Completed != previouslyCompleted {
			t.Fatalf("previouslyCompleted=%v unexpected result: %+v", previouslyCompleted, got)
		}
	}
}

func TestEvaluateOverflowBoundariesRejectNaturalEvidence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		state State
		input Input
	}{
		{
			name:  "received time subtraction overflow",
			state: State{LastPosition: 10, LastReceivedAtMS: math.MinInt64, LastSequence: 1, ValidPlaySeconds: 60},
			input: Input{FileType: "video", Duration: 100, Position: 95, Sequence: 2, ReceivedAtMS: math.MaxInt64, Event: EventProgress},
		},
		{
			name:  "position subtraction overflow",
			state: State{LastPosition: math.MinInt64, LastReceivedAtMS: 1_000, LastSequence: 1, ValidPlaySeconds: 60},
			input: Input{FileType: "video", Duration: 0, Position: math.MaxInt64, Sequence: 2, ReceivedAtMS: 2_000, Event: EventProgress},
		},
		{
			name:  "received ordering at opposite extremes",
			state: State{LastPosition: 93, LastReceivedAtMS: math.MaxInt64, LastSequence: 1, ValidPlaySeconds: 60},
			input: Input{FileType: "video", Duration: 100, Position: 95, Sequence: 2, ReceivedAtMS: math.MinInt64, Event: EventProgress},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.state, tc.input)
			if got.State.ValidPlaySeconds != tc.state.ValidPlaySeconds {
				t.Fatalf("evidence added at overflow boundary: got %v want %v", got.State.ValidPlaySeconds, tc.state.ValidPlaySeconds)
			}
			if got.Completed || got.AutoCompleted {
				t.Fatalf("invalid overflow segment accepted as natural: %+v", got)
			}
		})
	}
}

func TestEvaluateKnownEventsStillAdvanceState(t *testing.T) {
	t.Parallel()

	for _, event := range []Event{EventStart, EventProgress, EventSeek, EventEnded} {
		got := Evaluate(State{}, Input{FileType: "video", Duration: 100, Position: 10, Sequence: 1, ReceivedAtMS: 1_000, Event: event})
		if !got.AcceptPosition || got.Stale || got.State.LastSequence != 1 || got.State.LastPosition != 10 {
			t.Fatalf("event=%q known behavior changed: %+v", event, got)
		}
	}
}
func TestEvaluateEndedCompletesImmediatelyWithoutAutoCompletion(t *testing.T) {
	t.Parallel()

	got := Evaluate(State{}, Input{FileType: "audio", Duration: 0, Position: 12, Sequence: 1, ReceivedAtMS: 1_000, Event: EventEnded})
	if !got.Completed || got.AutoCompleted || !got.AcceptPosition || got.Stale {
		t.Fatalf("unexpected ended result: %+v", got)
	}
}

func TestEvaluatePreviouslyCompletedRemainsCompletedForEveryEvent(t *testing.T) {
	t.Parallel()

	for _, event := range []Event{EventStart, EventProgress, EventSeek, EventEnded} {
		t.Run(string(event), func(t *testing.T) {
			got := Evaluate(State{}, Input{FileType: "video", Duration: 100, Position: 1, Sequence: 1, ReceivedAtMS: 1_000, Event: event, PreviouslyCompleted: true})
			if !got.Completed || got.AutoCompleted {
				t.Fatalf("event=%q result=%+v", event, got)
			}
		})
	}
}

func TestEvaluateClampsAcceptedPositionOnlyForKnownDuration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		duration int64
		position int64
		want     int64
	}{
		{name: "known below zero", duration: 100, position: -5, want: 0},
		{name: "known in range", duration: 100, position: 42, want: 42},
		{name: "known above duration", duration: 100, position: 150, want: 100},
		{name: "unknown preserves positive", duration: 0, position: 150, want: 150},
		{name: "unknown clamps negative", duration: 0, position: -5, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(State{}, Input{FileType: "video", Duration: tc.duration, Position: tc.position, Sequence: 1, ReceivedAtMS: 1_000, Event: EventStart})
			if got.State.LastPosition != tc.want {
				t.Fatalf("position=%d duration=%d got=%d want=%d", tc.position, tc.duration, got.State.LastPosition, tc.want)
			}
		})
	}
}

func TestPublicConstantsHaveSpecifiedValues(t *testing.T) {
	t.Parallel()

	if MinValidPlaySeconds != 60.0 || MaxEvidenceGapMS != 30_000 || PositionToleranceSeconds != 5 || MaxPlaybackRate != 3.0 {
		t.Fatalf("unexpected constants: min=%v gap=%v tolerance=%v rate=%v", MinValidPlaySeconds, MaxEvidenceGapMS, PositionToleranceSeconds, MaxPlaybackRate)
	}
}

func assertResultState(t *testing.T, got Result, want State) {
	t.Helper()
	if !reflect.DeepEqual(got.State, want) {
		t.Fatalf("state = %+v, want %+v", got.State, want)
	}
}
