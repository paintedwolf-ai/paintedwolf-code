package briefing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	// ErrUnknownProfile reports a profile id no resolver is registered for.
	ErrUnknownProfile = errors.New("briefing profile is not registered")
	// ErrInvalidTarget reports a target that is malformed for its profile.
	ErrInvalidTarget = errors.New("briefing target is malformed")
	// ErrTargetUnavailable reports a well-formed target that cannot be
	// briefed now, such as one over its member bound.
	ErrTargetUnavailable = errors.New("briefing target is unavailable")
)

// Target is a wire target as the person asked for it, before resolution.
type Target struct {
	ProjectID  string
	ProjectDir string
	Ref        json.RawMessage
	Manual     bool
}

// Profile resolves wire targets of one kind into requests with bounded work
// and no model call. Resolvers validate every reference against host
// records; the client's descriptor is never trusted.
type Profile interface {
	ID() string
	Version() string
	Policy() Policy
	Resolve(ctx context.Context, target Target) (Request, error)
}

// Registry holds the profiles the briefing endpoint serves.
type Registry struct {
	mu       sync.RWMutex
	profiles map[string]Profile
}

func NewRegistry() *Registry { return &Registry{profiles: map[string]Profile{}} }

func (r *Registry) Register(profile Profile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.profiles[profile.ID()]; exists {
		return fmt.Errorf("briefing profile %q registered twice", profile.ID())
	}
	r.profiles[profile.ID()] = profile
	return nil
}

func (r *Registry) Profile(id string) (Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if profile, ok := r.profiles[id]; ok {
		return profile, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownProfile, id)
}

// IDs lists the registered profiles in a stable order.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.profiles))
	for id := range r.profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
