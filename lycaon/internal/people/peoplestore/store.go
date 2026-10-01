// Package peoplestore reads the people table.
package peoplestore

import (
	"context"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/pkg/api"
)

// Store reads people from one durable store.
type Store struct {
	queries *db.Queries

	// The owner row is immutable for the store's lifetime.
	ownerMu sync.Mutex
	owner   people.Person
}

// New reads people through database.
func New(database db.DBTX) *Store {
	return &Store{queries: db.New(database)}
}

// HostOwner returns the person holding the host's device credential.
func (s *Store) HostOwner(ctx context.Context) (people.Person, error) {
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	if s.owner.Valid() {
		return s.owner, nil
	}
	row, err := s.queries.GetHostOwner(ctx)
	if err != nil {
		return people.Person{}, fmt.Errorf("read host owner: %w", err)
	}
	s.owner = people.Person{ID: row.ID, Role: api.PersonRole(row.Role)}
	return s.owner, nil
}
