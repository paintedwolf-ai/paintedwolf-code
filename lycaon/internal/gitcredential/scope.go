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

// ErrUseNotGranted refuses a request for an admitted recipient when the
// invocation holds no grant to use saved credentials.
var ErrUseNotGranted = errors.New("the invocation may not use saved credentials")

// Scope limits one service to the recipients its invocation was admitted to
// contact, and to answering at all only once the person granted credential
// use. Recipient authority is not credential-store authority: a scoped
// service answers get only, so an agent's Git cannot write or remove the
// user's saved credentials.
type Scope struct {
	mu         sync.RWMutex
	recipients []Recipient
	granted    bool
	declined   []Recipient
}

// Admit adds recipients once their contact is authorized. Each request is
// checked against the scope at the time it arrives.
func (s *Scope) Admit(recipients ...Recipient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recipients = append(s.recipients, recipients...)
}

// Grant lets the service answer admitted recipients from saved credentials.
func (s *Scope) Grant() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.granted = true
}

// Declined lists the admitted recipients Git asked credentials for while no
// grant covered them.
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
	b.scope.mu.RLock()
	granted := b.scope.granted
	b.scope.mu.RUnlock()
	if !granted {
		b.scope.decline(recipient)
		return Credential{}, false, ErrUseNotGranted
	}
	if c.Username == "" {
		c.Username = recipient.Username
	}
	return b.inner.Get(c)
}

// Store and Erase answer without writing: Git reports a credential it used or
// one the server rejected, and an agent's Git never changes the user's store.
func (scopedBackend) Store(Credential) error { return nil }

func (scopedBackend) Erase(Credential) error { return nil }

// PlatformBackend is the host's own credential store: the macOS Keychain, or
// the vault elsewhere. A scoped service reads it on the helper's behalf, so the
// helper process itself never holds store access.
func PlatformBackend() Backend { return newBackend() }
