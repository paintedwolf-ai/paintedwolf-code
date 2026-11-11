package gitcredential

import (
	"errors"
	"slices"
	"strings"
	"sync"
)

// ErrOutOfScope refuses a helper request the served invocation was not
// admitted for. Nothing reaches the store.
var ErrOutOfScope = errors.New("credential request is outside the invocation's admitted scope")

// Scope limits one service to the recipients its invocation was admitted to
// contact. Recipient authority is not credential-store authority: a scoped
// service answers get only, so an agent's Git cannot write or remove the
// user's saved credentials.
type Scope struct {
	mu         sync.RWMutex
	recipients []Recipient
}

// Admit adds recipients once their contact is authorized. Each request is
// checked against the scope at the time it arrives.
func (s *Scope) Admit(recipients ...Recipient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recipients = append(s.recipients, recipients...)
}

// Recipient is a protocol and host as Git sends them; host keeps any port.
type Recipient struct {
	Protocol string
	Host     string
}

func (s *Scope) admits(c Credential) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	want := Recipient{Protocol: strings.ToLower(c.Protocol), Host: strings.ToLower(c.Host)}
	return want.Protocol != "" && want.Host != "" && slices.ContainsFunc(s.recipients, func(r Recipient) bool {
		return strings.EqualFold(r.Protocol, want.Protocol) && strings.EqualFold(r.Host, want.Host)
	})
}

type scopedBackend struct {
	inner Backend
	scope *Scope
}

func (b scopedBackend) Get(c Credential) (Credential, bool, error) {
	if !b.scope.admits(c) {
		return Credential{}, false, ErrOutOfScope
	}
	return b.inner.Get(c)
}

func (scopedBackend) Store(Credential) error { return ErrOutOfScope }

func (scopedBackend) Erase(Credential) error { return ErrOutOfScope }

// PlatformBackend is the host's own credential store: the macOS Keychain, or
// the vault elsewhere. A scoped service reads it on the helper's behalf, so the
// helper process itself never holds store access.
func PlatformBackend() Backend { return newBackend() }
