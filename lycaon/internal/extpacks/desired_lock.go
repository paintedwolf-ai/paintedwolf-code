package extpacks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// PrepareLockDesired rebuilds the device lock.
func PrepareLockDesired(ctx context.Context) (*PackagePlan, error) {
	desiredPath, err := DeviceDesiredPath()
	if err != nil {
		return nil, err
	}
	desired, err := LoadDesiredFile(desiredPath)
	if err != nil {
		return nil, err
	}
	graph, roots, err := resolveIntentGraph(ctx, desired, "", nil)
	if err != nil {
		return nil, err
	}
	if err := storePlanCandidates(graph); err != nil {
		cleanupCandidates(graph)
		return nil, err
	}
	return &PackagePlan{
		Roots:       roots,
		RebuildLock: true,
		graph:       graph,
	}, nil
}

func validateDesiredLock(desired DesiredState, lock LockFile) error {
	for _, row := range desired.Packs {
		if strings.TrimSpace(row.Source) == "" {
			continue
		}
		locked, ok := lock.Package(row.ID)
		if !ok {
			return fmt.Errorf("desired package %s has no exact lock entry; run extensions lock", row.ID)
		}
		if row.Development {
			abs, linked, err := resolveLinkedLocalSource(row.Source, "")
			if err != nil || !linked {
				return fmt.Errorf("desired package %s development source is unavailable", row.ID)
			}
			lockedSource, err := filepath.Abs(locked.Source)
			if err != nil || locked.Kind != PackKindPath || filepath.Clean(abs) != filepath.Clean(lockedSource) {
				return fmt.Errorf("desired package %s development source does not match lock", row.ID)
			}
			continue
		}
		if locked.Kind != PackKindGit || locked.Source != row.Source {
			return fmt.Errorf("desired package %s source or install mode does not match lock", row.ID)
		}
		if row.Ref != "" {
			if locked.Ref != row.Ref {
				return fmt.Errorf("desired package %s ref %s does not match locked ref %s", row.ID, row.Ref, locked.Ref)
			}
			continue
		}
		constraint, err := semver.NewConstraint(row.Version)
		version, versionErr := parseCanonicalVersion(locked.Version)
		if err != nil || versionErr != nil || !constraint.Check(version) {
			return fmt.Errorf("desired package %s version %s does not admit locked version %s", row.ID, row.Version, locked.Version)
		}
	}
	return nil
}

func reachableLock(lock LockFile, desired DesiredState) (LockFile, error) {
	byID := map[string]LockedPackage{}
	for _, pkg := range lock.Packages {
		byID[pkg.ID] = pkg
	}
	reachable := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if reachable[id] {
			return
		}
		pkg, ok := byID[id]
		if !ok {
			return
		}
		reachable[id] = true
		for dependencyID := range pkg.Dependencies {
			if !IsStockPackID(dependencyID) {
				visit(dependencyID)
			}
		}
	}
	for _, root := range desired.Packs {
		visit(root.ID)
	}
	if len(reachable) != len(lock.Packages) {
		return LockFile{}, fmt.Errorf("extension lock contains packages unreachable from desired roots; run extensions lock")
	}
	return lock, nil
}
