package runstate

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type WorkerCancelScope string

const (
	WorkerCancelNone    WorkerCancelScope = "none"
	WorkerCancelRunning WorkerCancelScope = "running"
	WorkerCancelAll     WorkerCancelScope = "all"
)

type TeardownIntent struct {
	ID              string
	RunID           string
	SourceRevision  int64
	CancelScope     WorkerCancelScope
	AbortDelegation bool
	Reason          string
}

func NewTeardownIntent(runID string, revision int64, scope WorkerCancelScope, abort bool, reason string) *TeardownIntent {
	if scope == WorkerCancelNone && !abort {
		return nil
	}
	return &TeardownIntent{
		ID:    uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("workflow-teardown:%s:%d", runID, revision))).String(),
		RunID: runID, SourceRevision: revision, CancelScope: scope,
		AbortDelegation: abort, Reason: strings.TrimSpace(reason),
	}
}
