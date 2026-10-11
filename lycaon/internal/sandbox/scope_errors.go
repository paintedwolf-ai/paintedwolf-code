package sandbox

import (
	"errors"
	"fmt"
)

// ErrPathEscape identifies lexical or symbolic-link escape from a project boundary.
var ErrPathEscape = errors.New("path escapes project boundary")

// ScopeKind distinguishes read vs write path scope violations.
type ScopeKind int

const (
	ScopeRead ScopeKind = iota
	ScopeWrite
)

// ScopeError is returned when a path is outside a profile's read or write scope.
type ScopeError struct {
	Path    string
	Profile string
	Kind    ScopeKind
}

func (e *ScopeError) Error() string {
	if e == nil {
		return "outside profile scope"
	}
	switch e.Kind {
	case ScopeRead:
		return fmt.Sprintf("path %q outside profile read scope", e.Path)
	default:
		return fmt.Sprintf("path %q outside profile write scope", e.Path)
	}
}

func newOutsideReadScope(path, profile string) error {
	return &ScopeError{Path: path, Profile: profile, Kind: ScopeRead}
}

func newOutsideWriteScope(path, profile string) error {
	return &ScopeError{Path: path, Profile: profile, Kind: ScopeWrite}
}
