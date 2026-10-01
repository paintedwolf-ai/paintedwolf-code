package store

import (
	"context"

	"github.com/lycaon/lycaon/internal/people"
)

// HostOwner returns the person holding the host's device credential.
func (s *SQL) HostOwner(ctx context.Context) (people.Person, error) {
	return s.peopleStore.HostOwner(ctx)
}

// HostOwner returns the store's fixed owner.
func (s *Memory) HostOwner(context.Context) (people.Person, error) {
	return s.owner, nil
}
