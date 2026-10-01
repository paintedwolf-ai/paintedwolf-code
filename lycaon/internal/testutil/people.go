package testutil

import (
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/pkg/api"
)

// HostOwner is a fixed owner for tests that need an authenticated person
// without a store behind it.
func HostOwner() people.Person {
	return people.Person{ID: "00000000-0000-4000-8000-00000000000a", Role: api.PersonRoleOwner}
}
