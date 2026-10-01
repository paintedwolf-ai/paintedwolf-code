package extpacks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// PackageChange is one exact lock transition in an update plan.
type PackageChange struct {
	PackID            string
	CurrentVersion    string
	CandidateVersion  string
	CurrentRevision   string
	CandidateRevision string
	Kind              string
}

// UpdateCheck is the complete candidate graph comparison for one root package.
type UpdateCheck struct {
	PackID            string
	Available         bool
	CurrentVersion    string
	CandidateVersion  string
	CurrentRevision   string
	CandidateRevision string
	Changes           []PackageChange
	Message           string
}

// ResolutionError records a rejected candidate graph for host diagnostics.
type ResolutionError struct{ Err error }

func (e *ResolutionError) Error() string { return e.Err.Error() }

func (e *ResolutionError) Unwrap() error { return e.Err }

// CheckUpdate resolves a candidate without mutating cache, intent, or lock state.
func CheckUpdate(ctx context.Context, packID string) (UpdateCheck, error) {
	desired, locked, root, err := packageUpdateState(packID)
	var dependency *DependencyPackError
	switch {
	case errors.Is(err, ErrStockPack):
		return UpdateCheck{PackID: packID, Message: "stock packs update with the app"}, nil
	case errors.As(err, &dependency):
		return UpdateCheck{PackID: packID, CurrentVersion: dependency.Locked.Version, Message: "dependencies update with the packs that require them"}, nil
	case err != nil:
		return UpdateCheck{}, err
	}
	if root.Development {
		return UpdateCheck{PackID: packID, CurrentVersion: locked.Version, Message: "development packs reload from disk"}, nil
	}
	_, graph, err := resolveDeviceGraph(ctx, "", nil, nil, []string{packID})
	if err != nil {
		if OperationalFailure(err) {
			return UpdateCheck{}, err
		}
		return UpdateCheck{}, &ResolutionError{Err: err}
	}
	defer cleanupCandidates(graph)
	if _, ok := graph[packID]; !ok {
		return UpdateCheck{}, fmt.Errorf("update: resolved scope omits %q", packID)
	}
	lockPath, err := DeviceLockPath()
	if err != nil {
		return UpdateCheck{}, err
	}
	current, err := LoadLockFile(lockPath)
	if err != nil {
		return UpdateCheck{}, err
	}
	candidate := lockForGraph(current, desired, graph)
	changes := LockChanges(current, candidate)
	rootCandidate := graph[packID]
	available := false
	for _, change := range changes {
		if change.Kind != "unchanged" {
			available = true
			break
		}
	}
	message := "up to date"
	if available {
		message = fmt.Sprintf("%d package change(s) available", countChanged(changes))
	}
	return UpdateCheck{
		PackID: packID, Available: available,
		CurrentVersion: locked.Version, CandidateVersion: rootCandidate.Manifest.Version,
		CurrentRevision: locked.Revision, CandidateRevision: rootCandidate.Revision,
		Changes: changes, Message: message,
	}, nil
}

func packageUpdateState(packID string) (DesiredState, LockedPackage, DesiredPack, error) {
	packID = strings.TrimSpace(packID)
	if packID == "" {
		return DesiredState{}, LockedPackage{}, DesiredPack{}, fmt.Errorf("pack id required")
	}
	if IsStockPackID(packID) {
		return DesiredState{}, LockedPackage{}, DesiredPack{}, fmt.Errorf("%w: stock packs update with the app", ErrStockPack)
	}
	desiredPath, err := DeviceDesiredPath()
	if err != nil {
		return DesiredState{}, LockedPackage{}, DesiredPack{}, err
	}
	lockPath, err := DeviceLockPath()
	if err != nil {
		return DesiredState{}, LockedPackage{}, DesiredPack{}, err
	}
	desired, err := LoadDesiredFile(desiredPath)
	if err != nil {
		return DesiredState{}, LockedPackage{}, DesiredPack{}, err
	}
	lock, err := LoadLockFile(lockPath)
	if err != nil {
		return DesiredState{}, LockedPackage{}, DesiredPack{}, err
	}
	locked, ok := lock.Package(packID)
	if !ok {
		return DesiredState{}, LockedPackage{}, DesiredPack{}, fmt.Errorf("%w: %s missing from lock", ErrPackNotInstalled, packID)
	}
	root, ok := DesiredPackRow(desired, packID)
	if !ok {
		return DesiredState{}, LockedPackage{}, DesiredPack{}, &DependencyPackError{PackID: packID, Locked: locked}
	}
	return desired, locked, root, nil
}

// DependencyPackError names an installed pack that only another pack
// requires; it changes when the requiring pack does.
type DependencyPackError struct {
	PackID string
	Locked LockedPackage
}

func (e *DependencyPackError) Error() string {
	return fmt.Sprintf("pack %s is not a direct device installation", e.PackID)
}

func lockForGraph(current LockFile, desired DesiredState, graph map[string]packageCandidate) LockFile {
	next := current
	for _, candidate := range graph {
		next = upsertLockedPackage(next, candidateLockedPackage(candidate, graph))
	}
	return retainReachablePackages(next, desired)
}

// LockChanges is the package-level diff between two locks, unchanged rows
// included.
func LockChanges(current, candidate LockFile) []PackageChange {
	currentByID := map[string]LockedPackage{}
	candidateByID := map[string]LockedPackage{}
	ids := map[string]bool{}
	for _, pkg := range current.Packages {
		currentByID[pkg.ID] = pkg
		ids[pkg.ID] = true
	}
	for _, pkg := range candidate.Packages {
		candidateByID[pkg.ID] = pkg
		ids[pkg.ID] = true
	}
	changes := make([]PackageChange, 0, len(ids))
	for id := range ids {
		before, hadBefore := currentByID[id]
		after, hasAfter := candidateByID[id]
		kind := "unchanged"
		switch {
		case !hadBefore:
			kind = "added"
		case !hasAfter:
			kind = "removed"
		case before.Version != after.Version:
			left, _ := parseCanonicalVersion(before.Version)
			right, _ := parseCanonicalVersion(after.Version)
			if right.LessThan(left) {
				kind = "downgraded"
			} else {
				kind = "upgraded"
			}
		case before.Revision != after.Revision || before.Integrity != after.Integrity:
			kind = "revised"
		}
		changes = append(changes, PackageChange{
			PackID: id, CurrentVersion: before.Version, CandidateVersion: after.Version,
			CurrentRevision: before.Revision, CandidateRevision: after.Revision, Kind: kind,
		})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].PackID < changes[j].PackID })
	return changes
}

func countChanged(changes []PackageChange) int {
	count := 0
	for _, change := range changes {
		if change.Kind != "unchanged" {
			count++
		}
	}
	return count
}
