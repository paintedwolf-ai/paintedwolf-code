package extensionstate

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/extpacks"
)

// RemovalState is one consistent device intent and dependency snapshot.
type RemovalState struct {
	Revision string
	Desired  extpacks.DesiredState
	Lock     extpacks.LockFile
}

func (o *Owner) RemovalState() (RemovalState, error) {
	snap, err := takeStateSnapshot("")
	if err != nil {
		return RemovalState{}, err
	}
	device, _, err := parseStates(snap)
	return RemovalState{Revision: snap.Revision, Desired: device.Desired, Lock: device.Lock}, err
}

// RemoveReviewedOp rechecks references before one batch publication.
type RemoveReviewedOp struct {
	PackIDs    []string
	Revalidate func(context.Context) error
}

func (op RemoveReviewedOp) prepare(_ context.Context, _ *Owner, _ Scope) (*prepared, error) {
	if op.Revalidate == nil {
		return nil, fmt.Errorf("reviewed removal requires reference validation")
	}
	plan, err := extpacks.PrepareRemoval(op.PackIDs)
	if err != nil {
		return nil, err
	}
	return &prepared{plan: plan, beforeCommit: op.Revalidate}, nil
}
