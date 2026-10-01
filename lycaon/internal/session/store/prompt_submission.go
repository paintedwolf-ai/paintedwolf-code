package store

import (
	"errors"
	"fmt"
	"time"
)

var ErrPromptSubmissionNotFound = errors.New("prompt submission not found")

// ErrPromptSubmissionAuthorship rejects a user prompt without a sender or a
// host turn with one.
var ErrPromptSubmissionAuthorship = errors.New("prompt submission authorship does not match its origin")

// ErrPromptAttachmentRetained reports an active history reference.
var ErrPromptAttachmentRetained = errors.New("prompt attachment is retained")

// PromptSubmissionStatus is the durable lifecycle of one admitted turn.
type PromptSubmissionStatus string

const (
	PromptSubmissionQueued      PromptSubmissionStatus = "queued"
	PromptSubmissionRunning     PromptSubmissionStatus = "running"
	PromptSubmissionComplete    PromptSubmissionStatus = "complete"
	PromptSubmissionFailed      PromptSubmissionStatus = "failed"
	PromptSubmissionInterrupted PromptSubmissionStatus = "interrupted"
	// PromptSubmissionCanceled is terminal without a claim.
	PromptSubmissionCanceled PromptSubmissionStatus = "canceled"
)

// Terminal reports whether no further receipt transition is valid.
func (s PromptSubmissionStatus) Terminal() bool {
	switch s {
	case PromptSubmissionComplete, PromptSubmissionFailed, PromptSubmissionInterrupted, PromptSubmissionCanceled:
		return true
	default:
		return false
	}
}

// PromptSubmissionOrigin determines recovery behavior for an admitted turn.
type PromptSubmissionOrigin string

const (
	// PromptSubmissionOriginUser is a turn admitted by the HTTP prompt path.
	PromptSubmissionOriginUser PromptSubmissionOrigin = "user"
	// PromptSubmissionOriginLoopWake is a coordinator loop wake.
	PromptSubmissionOriginLoopWake PromptSubmissionOrigin = "loop_wake"
	// PromptSubmissionOriginWorkerCloseout is a worker survey closeout or trim kick.
	PromptSubmissionOriginWorkerCloseout PromptSubmissionOrigin = "worker_closeout"
	// PromptSubmissionOriginGroundingRetry is a citation grounding retry kick.
	PromptSubmissionOriginGroundingRetry PromptSubmissionOrigin = "grounding_retry"
)

// HostInitiated reports whether the host asked for this turn rather than a client.
func (o PromptSubmissionOrigin) HostInitiated() bool {
	switch o {
	case PromptSubmissionOriginLoopWake, PromptSubmissionOriginWorkerCloseout, PromptSubmissionOriginGroundingRetry:
		return true
	default:
		return false
	}
}

// Valid reports whether the origin is one the store's CHECK constraint accepts.
func (o PromptSubmissionOrigin) Valid() bool {
	switch o {
	case PromptSubmissionOriginUser, PromptSubmissionOriginLoopWake,
		PromptSubmissionOriginWorkerCloseout, PromptSubmissionOriginGroundingRetry:
		return true
	}
	return false
}

// PromptSubmission is the receipt written before asynchronous prompt work begins.
type PromptSubmission struct {
	ID                string
	SessionID         string
	AdmissionSeq      int64
	ProjectID         string
	InputDigest       string
	InputJSON         string
	Origin            PromptSubmissionOrigin
	Status            PromptSubmissionStatus
	ClaimToken        string
	ResultJSON        string
	Error             string
	CreatedAt         time.Time
	StartedAt         *time.Time
	CompletedAt       *time.Time
	AttachmentBlobIDs []string
	ErrorCode         string
	// SubmittedBy is the person who sent a user prompt; empty for host turns.
	SubmittedBy string
}

// validateAuthorship requires a sender on user prompts and none on host turns.
func (p PromptSubmission) validateAuthorship() error {
	if !p.Origin.Valid() {
		return fmt.Errorf("invalid prompt submission origin %q", p.Origin)
	}
	if (p.Origin == PromptSubmissionOriginUser) != (p.SubmittedBy != "") {
		return fmt.Errorf("prompt submission origin %q with submitter %q: %w", p.Origin, p.SubmittedBy, ErrPromptSubmissionAuthorship)
	}
	return nil
}

// replays reports whether in is the same admission as the stored receipt.
func (p PromptSubmission) replays(in PromptSubmission) bool {
	return p.SessionID == in.SessionID && p.ProjectID == in.ProjectID &&
		p.InputDigest == in.InputDigest && p.Origin == in.Origin && p.SubmittedBy == in.SubmittedBy
}

// PromptSubmissionInputUpdate replaces one queued receipt input.
type PromptSubmissionInputUpdate struct {
	ID        string
	InputJSON string
}

// PromptSubmissionFailure is the terminal error recorded for a submission.
// Code preserves the classification the raised error carried.
type PromptSubmissionFailure struct {
	Message string
	Code    string
}

// PromptAttachmentRetention is one durable blob-retention claim.
type PromptAttachmentRetention struct {
	ProjectID   string
	OperationID string
	BlobID      string
}

// PromptSubmissionConflictError reports conflicting operation input.
type PromptSubmissionConflictError struct {
	ID string
}

func (e *PromptSubmissionConflictError) Error() string {
	return "prompt submission " + e.ID + " was already used for different input"
}
