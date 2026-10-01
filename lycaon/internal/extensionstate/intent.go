// Package extensionstate settles extension mutation transactions.
package extensionstate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
)

// Scope names the mutation target.
type Scope struct {
	Kind       string // device | project
	ProjectID  string
	ProjectDir string
}

func (s Scope) normalize() (Scope, error) {
	s.Kind = strings.TrimSpace(s.Kind)
	switch s.Kind {
	case "device":
		return s, nil
	case "project":
		if strings.TrimSpace(s.ProjectDir) == "" {
			return Scope{}, fmt.Errorf("project scope requires a project directory")
		}
		return s, nil
	default:
		return Scope{}, fmt.Errorf("unknown scope %q (want project|device)", s.Kind)
	}
}

// contextDir is the project directory visible to state reads for this scope.
func (s Scope) contextDir() string {
	if s.Kind == "project" {
		return s.ProjectDir
	}
	return ""
}

// Intent is one complete mutation submission.
type Intent struct {
	Scope            Scope
	ExpectedRevision string
	Op               Op
}

// Op is the closed mutation vocabulary.
type Op interface {
	prepare(context.Context, *Owner, Scope) (*prepared, error)
}

// catalogChecked validates ids against the candidate catalog.
type catalogChecked interface {
	checkAgainst(scope Scope, eff *extpacks.EffectiveCatalog) error
}

// ErrUnknownUnit and ErrUnknownPack name an id no pack in the catalog provides.
var (
	ErrUnknownUnit = errors.New("no pack in this catalog provides that unit")
	ErrUnknownPack = errors.New("no such pack in this catalog")
	// ErrProjectMutationUnsupported limits project state to unit disables.
	ErrProjectMutationUnsupported    = errors.New("project extension state may only disable units")
	ErrProjectUnitDisableUnsupported = errors.New("this unit cannot be disabled in project extension state")
)

type InstallOp struct {
	Source, Version, Ref string
	// ExpectedPackID, when set, binds the resolved package to a reviewed suggestion.
	ExpectedPackID string
}
type InstallMetaOp struct{ Source, Version, Ref string }
type RemoveOp struct{ PackID string }
type RemoveMetaOp struct{ MetaPackID string }
type UpdateOp struct{ PackID string }
type ReloadOp struct{ PackID string }
type LockOp struct{}
type SetPackEnabledOp struct {
	PackID  string
	Enabled bool
}
type SetUnitDisabledOp struct {
	UnitID   string
	Disabled bool
}

// SetUnitOwnOp sets or clears the pack selected by `own:`.
type SetUnitOwnOp struct {
	UnitID string
	PackID *string
}

// UpdateUnitOp commits the requested enablement and ownership together.
type UpdateUnitOp struct {
	UnitID    string
	Enabled   *bool
	OwnSet    bool
	OwnPackID string
}

type ApplyProfileOp struct{ PackID, Profile string }
type ApplyMetaOp struct {
	MetaPackID string
	Enable     bool
}
type SetConfigurationOp struct {
	Packs map[string]map[string]any
}

// UndeclineOp removes device-wide suggestion declines.
type UndeclineOp struct{ PackIDs []string }

// SetInstalledFromOp records which project accepted a suggestion.
type SetInstalledFromOp struct{ PackID, ProjectID string }

// Result is the committed outcome of one intent.
type Result struct {
	Revision    string
	DesiredPath string
	Desired     extpacks.DesiredState // the mutated scope's committed state
	Warnings    []string
	Install     *extpacks.PackageResolution
	PackageRoot string
	Meta        *extpacks.InstallMetaResult
	// PackageChanges includes transitive lock changes even when the root is unchanged.
	PackageChanges []extpacks.PackageChange
}

// ErrExpectedRevisionRequired rejects mutations without an optimistic token.
var ErrExpectedRevisionRequired = errors.New("extension mutation requires expected_revision")

// StaleError reports an expected-revision mismatch; nothing was committed.
type StaleError struct {
	Current string
}

func (e *StaleError) Error() string {
	return "extension state changed since the expected revision"
}
