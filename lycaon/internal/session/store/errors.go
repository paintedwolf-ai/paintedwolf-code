// Package store persists sessions and related ledgers.
package store

import (
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

// ErrSessionNotFound is returned when a session id is unknown.
var ErrSessionNotFound = errors.New("session not found")

// ErrMessageNotFound is returned when a message id is not in a session's transcript.
var ErrMessageNotFound = errors.New("message not found")

// ErrDuplicateMessageID is returned when AppendMessages is asked to insert an
// id that already exists in the transcript or twice in the same batch.
var ErrDuplicateMessageID = errors.New("duplicate message id")

// ErrSessionArchived rejects pinning a chat that is out of the working set.
var ErrSessionArchived = errors.New("archived chats cannot be pinned")

// ErrSessionNotPinned rejects moving a chat that has no pinned position.
var ErrSessionNotPinned = errors.New("chat is not pinned")

// ErrWorkerChildPin rejects pinning a worker child session.
var ErrWorkerChildPin = errors.New("worker child sessions cannot be pinned")

// ErrSessionOwnerRequired rejects a session with no owning person.
var ErrSessionOwnerRequired = errors.New("session owner is required")

// ErrSessionModelRefIncomplete identifies an unpaired provider or model.
var ErrSessionModelRefIncomplete = errors.New("provider_id and model must be set together")

// SessionStatusTransitionError identifies an illegal lifecycle edge.
type SessionStatusTransitionError struct {
	From api.SessionStatus
	To   api.SessionStatus
}

func (e *SessionStatusTransitionError) Error() string {
	return fmt.Sprintf("invalid session status transition %q to %q", e.From, e.To)
}
