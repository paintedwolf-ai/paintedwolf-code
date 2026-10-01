// Package people names the humans who act on this host and what each may do.
//
// Durable authorship records a person id; the window or tab a person used is a
// separate client id.
package people

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/pkg/api"
)

// Person is an authenticated human caller.
type Person struct {
	ID   string
	Role api.PersonRole
}

// Valid reports whether p names a stored person with a role.
func (p Person) Valid() bool {
	return p.ID != "" && p.Role != ""
}

// Wire projects p for API responses.
func (p Person) Wire() api.Person {
	return api.Person{ID: p.ID, Role: p.Role}
}

type callerKey struct{}

// WithCaller binds the authenticated person to a request context.
func WithCaller(ctx context.Context, p Person) context.Context {
	return context.WithValue(ctx, callerKey{}, p)
}

// Caller returns the person bound by WithCaller.
func Caller(ctx context.Context) (Person, bool) {
	p, ok := ctx.Value(callerKey{}).(Person)
	return p, ok && p.Valid()
}

// MayInvoke reports whether p may call the named API operation.
func MayInvoke(p Person, operationID string) bool {
	if !p.Valid() || operationID == "" {
		return false
	}
	switch p.Role {
	case api.PersonRoleOwner:
		return true
	default:
		return false
	}
}

// MayObserveEvents reports whether p may receive host events.
func MayObserveEvents(p Person) bool {
	if !p.Valid() {
		return false
	}
	switch p.Role {
	case api.PersonRoleOwner:
		return true
	default:
		return false
	}
}

// OwnerSource reads the host owner.
type OwnerSource interface {
	HostOwner(ctx context.Context) (Person, error)
}

// ErrNoDecidingPerson reports a decision record reached without an
// authenticated caller to name.
var ErrNoDecidingPerson = errors.New("no authenticated person is deciding")

// WithoutCaller removes the caller binding. Agent work runs on the context of
// the request that started the turn; stripping the binding keeps the agent's
// effects from being recorded as that person's.
func WithoutCaller(ctx context.Context) context.Context {
	return context.WithValue(ctx, callerKey{}, Person{})
}

// Deciding returns the person whose request is settling a decision.
func Deciding(ctx context.Context) (Person, error) {
	if caller, ok := Caller(ctx); ok {
		return caller, nil
	}
	return Person{}, ErrNoDecidingPerson
}

// Acting returns the caller bound to ctx, or the host owner for work that no
// authenticated request started. Records that name who decided use Deciding.
func Acting(ctx context.Context, owners OwnerSource) (Person, error) {
	if caller, ok := Caller(ctx); ok {
		return caller, nil
	}
	return owners.HostOwner(ctx)
}
