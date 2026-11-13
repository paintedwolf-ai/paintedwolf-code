package gitcredential

import (
	"errors"
	"path"
	"slices"
	"strings"
	"sync"
)

// ErrOutOfScope refuses a helper request the served invocation was not
// admitted for. Nothing reaches the store.
var ErrOutOfScope = errors.New("credential request is outside the invocation's admitted scope")

// ErrReleaseDeclined refuses a request for an admitted recipient whose saved
// credential the release review did not let leave.
var ErrReleaseDeclined = errors.New("the saved credential was not released to its recipient")

// ReleaseFunc decides whether a saved credential found for an admitted
// recipient may be handed to Git for it.
type ReleaseFunc func(Recipient, Credential) bool

// Scope limits one service to the recipients its invocation was admitted to
// contact, and answers one only once its release decides the found credential
// may go there. Recipient authority is not credential-store authority: a
// scoped service answers get only, so an agent's Git cannot write or remove
// the user's saved credentials.
type Scope struct {
	// Release reviews each found credential; nil releases nothing.
	Release ReleaseFunc

	mu         sync.RWMutex
	recipients []Recipient
	released   []Recipient
	declined   []Recipient
}

// Admit adds recipients once their contact is authorized. Each request is
// checked against the scope at the time it arrives.
func (s *Scope) Admit(recipients ...Recipient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recipients = append(s.recipients, recipients...)
}

// Declined lists the admitted recipients whose found credential the release
// did not let leave.
func (s *Scope) Declined() []Recipient {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.declined)
}

// Recipient is what an admitted URL names as Git sends it: protocol, host
// with any port, and the user and repository path when the URL carries them.
type Recipient struct {
	Protocol string
	Host     string
	Username string
	Path     string
}

func (r Recipient) covers(c Credential) bool {
	if !strings.EqualFold(r.Protocol, c.Protocol) || !strings.EqualFold(r.Host, c.Host) {
		return false
	}
	if r.Username != "" && c.Username != "" && r.Username != c.Username {
		return false
	}
	// Git sends a path only under credential.useHttpPath.
	return r.Path == "" || c.Path == "" || cleanPath(r.Path) == cleanPath(c.Path)
}

func cleanPath(p string) string { return strings.Trim(path.Clean("/"+p), "/") }

func (s *Scope) admitted(c Credential) (Recipient, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c.Protocol == "" || c.Host == "" {
		return Recipient{}, false
	}
	for _, r := range s.recipients {
		if r.covers(c) {
			return r, true
		}
	}
	return Recipient{}, false
}

func (s *Scope) decline(r Recipient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.declined, r) {
		s.declined = append(s.declined, r)
	}
}

type scopedBackend struct {
	inner Backend
	scope *Scope
}

func (b scopedBackend) Get(c Credential) (Credential, bool, error) {
	recipient, ok := b.scope.admitted(c)
	if !ok {
		return Credential{}, false, ErrOutOfScope
	}
	if c.Username == "" {
		c.Username = recipient.Username
	}
	// Nothing saved means nothing to release, so no one is asked.
	found, ok, err := b.inner.Get(c)
	if err != nil || !ok {
		return Credential{}, false, err
	}
	if !b.scope.release(recipient, found) {
		b.scope.decline(recipient)
		return Credential{}, false, ErrReleaseDeclined
	}
	return found, true, nil
}

// release asks once per recipient for the life of the invocation; Git asks
// again when it retries a request.
func (s *Scope) release(r Recipient, c Credential) bool {
	s.mu.RLock()
	done := slices.Contains(s.released, r)
	s.mu.RUnlock()
	if done {
		return true
	}
	if s.Release == nil || !s.Release(r, c) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.released, r) {
		s.released = append(s.released, r)
	}
	return true
}

// Store and Erase answer without writing: Git reports a credential it used or
// one the server rejected, and an agent's Git never changes the user's store.
func (scopedBackend) Store(Credential) error { return nil }

func (scopedBackend) Erase(Credential) error { return nil }
