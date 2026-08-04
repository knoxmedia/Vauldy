package retirement

import (
	"errors"
	"fmt"
	"time"
)

// Durable plaintext-retirement states. Retirement is outside the frozen DAG.
type State string

const (
	StateBlocked          State = "blocked"
	StateReady            State = "ready"
	StateQuarantining     State = "quarantining"
	StateQuarantined      State = "quarantined"
	StateDeleting         State = "deleting"
	StateVerified         State = "verified"
	StateRetryableFailed  State = "retryable_failed"
	StateOperatorRequired State = "operator_required"
)

// BlockerCode explains why retirement remains blocked.
type BlockerCode string

const (
	BlockerNone                   BlockerCode = ""
	BlockerPlanNotTerminal        BlockerCode = "plan_not_terminal"
	BlockerGenerationFence        BlockerCode = "generation_mismatch"
	BlockerFingerprintFence       BlockerCode = "source_fingerprint_mismatch"
	BlockerSourceMissing          BlockerCode = "source_missing"
	BlockerCiphertextUnreadable   BlockerCode = "ciphertext_unreadable"
	BlockerKeyUnreadable          BlockerCode = "key_unreadable"
	BlockerEvidenceUnreadable     BlockerCode = "evidence_unreadable"
	BlockerPackageOutputUnread    BlockerCode = "package_output_unreadable"
	BlockerPackageKeyUnreadable   BlockerCode = "package_key_unreadable"
	BlockerStrategyIncomplete     BlockerCode = "encrypted_source_strategy_incomplete"
	BlockerPolicyDisabled         BlockerCode = "cleanup_policy_disabled"
	BlockerActiveConsumer         BlockerCode = "active_plaintext_consumer"
	BlockerNoIntent               BlockerCode = "no_retirement_intent"
	BlockerSuperseded             BlockerCode = "generation_superseded"
)

const (
	BasisEncryption = "encryption"
	BasisPackage    = "package"
)

// DefaultMaxAttempts before escalation to operator_required.
const DefaultMaxAttempts = 5

// DefaultLeaseTTL is the worker lease duration.
const DefaultLeaseTTL = 60 * time.Second

// Identity fences a retirement attempt to media/generation/retry/attempt.
type Identity struct {
	RetirementID      int64
	MediaID           int64
	RunID             int64
	Generation        int64
	RetryRound        int
	Attempt           int
	SourcePath        string
	SourceFingerprint string
	BasisKind         string
	BasisID           int64
	EncryptionStageID string
	PackageTaskID     int64
}

// Row is the durable retirement projection.
type Row struct {
	Identity
	State                State
	BlockerCode          BlockerCode
	LeaseOwner           string
	LeaseUntil           *time.Time
	Attempts             int
	LastError            string
	QuarantinePath       string
	QuarantineFingerprint string
	NextRetryAt          *time.Time
}

// BarrierResult is the authoritative eligibility decision.
type BarrierResult struct {
	Eligible bool
	Blocker  BlockerCode
	Detail   string
}

// ActiveConsumerFunc reports whether any live consumer still holds plaintext.
type ActiveConsumerFunc func(mediaID int64) bool

var (
	ErrInvalidIdentity      = errors.New("retirement: invalid identity")
	ErrUnsafeQuarantinePath = errors.New("retirement: unsafe quarantine path")
	ErrLeaseLost            = errors.New("retirement: lease lost")
	ErrNotClaimable         = errors.New("retirement: not claimable")
	ErrBarrierBlocked       = errors.New("retirement: barrier blocked")
	ErrFingerprintMismatch  = errors.New("retirement: source fingerprint mismatch")
	ErrVerificationFailed   = errors.New("retirement: verification failed")
)

func (s State) IsTerminal() bool {
	return s == StateVerified || s == StateOperatorRequired
}

func (s State) IsInFlight() bool {
	return s == StateQuarantining || s == StateQuarantined || s == StateDeleting
}

func (s State) AllowsBarrierFlip() bool {
	return s == StateBlocked || s == StateReady
}

func formatErr(code string, err error) string {
	if err == nil {
		return code
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return fmt.Sprintf("%s: %s", code, msg)
}
