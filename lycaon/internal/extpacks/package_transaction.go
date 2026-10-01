package extpacks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolveDeviceGraph(
	ctx context.Context,
	sourceBaseDir string,
	additions []DesiredPack,
	seeded map[string]packageCandidate,
	subjectPacks []string,
) ([]DesiredPack, map[string]packageCandidate, error) {
	desiredPath, err := DeviceDesiredPath()
	if err != nil {
		return nil, nil, err
	}
	desired, err := LoadDesiredFile(desiredPath)
	if err != nil {
		return nil, nil, err
	}
	for _, addition := range additions {
		desired = setPackRow(desired, addition)
	}
	subjects := subjectSet(additions, subjectPacks)
	fixedRoots := make(map[string]packageCandidate, len(additions)+len(subjects))
	for _, addition := range additions {
		if candidate, ok := seeded[addition.ID]; ok {
			fixedRoots[addition.ID] = candidate
		}
	}
	if len(subjects) > 0 {
		held, err := pinHeldLockCandidates(desired, subjects)
		if err != nil {
			return nil, nil, err
		}
		for id, candidate := range held {
			if _, ok := fixedRoots[id]; ok {
				continue
			}
			fixedRoots[id] = candidate
		}
	}
	graph, roots, err := resolveIntentGraph(ctx, desired, sourceBaseDir, fixedRoots)
	if err != nil {
		return nil, nil, err
	}
	return roots, graph, nil
}

func subjectSet(additions []DesiredPack, subjectPacks []string) map[string]bool {
	out := make(map[string]bool, len(additions)+len(subjectPacks))
	for _, addition := range additions {
		if id := strings.TrimSpace(addition.ID); id != "" {
			out[id] = true
		}
	}
	for _, id := range subjectPacks {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

// pinHeldLockCandidates keeps unrelated installed roots fixed.
func pinHeldLockCandidates(
	desired DesiredState,
	subjects map[string]bool,
) (map[string]packageCandidate, error) {
	lockPath, err := DeviceLockPath()
	if err != nil {
		return nil, err
	}
	lock, err := LoadLockFile(lockPath)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]LockedPackage, len(lock.Packages))
	for _, pkg := range lock.Packages {
		byID[pkg.ID] = pkg
	}
	heldIDs := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if heldIDs[id] || subjects[id] {
			return nil
		}
		pkg, ok := byID[id]
		if !ok {
			return fmt.Errorf("held pack %s has no lock entry", id)
		}
		heldIDs[id] = true
		for depID := range pkg.Dependencies {
			if IsStockPackID(depID) {
				continue
			}
			if err := visit(depID); err != nil {
				return err
			}
		}
		return nil
	}
	for _, row := range desired.Packs {
		if subjects[row.ID] || strings.TrimSpace(row.Source) == "" {
			continue
		}
		if err := visit(row.ID); err != nil {
			return nil, err
		}
	}
	held := make(map[string]packageCandidate, len(heldIDs))
	for id := range heldIDs {
		candidate, err := candidateFromLocked(byID[id])
		if err != nil {
			return nil, fmt.Errorf("held pack %s: %w", id, err)
		}
		held[id] = candidate
	}
	return held, nil
}

func candidateFromLocked(pkg LockedPackage) (packageCandidate, error) {
	switch pkg.Kind {
	case PackKindPath:
		abs, err := filepath.Abs(pkg.Source)
		if err != nil {
			return packageCandidate{}, err
		}
		man, err := LoadManifest(abs)
		if err != nil {
			return packageCandidate{}, err
		}
		integrity, err := PackTreeIntegrity(abs)
		if err != nil {
			return packageCandidate{}, err
		}
		if integrity != pkg.Integrity {
			return packageCandidate{}, fmt.Errorf("path integrity differs from the lock")
		}
		return packageCandidate{
			Manifest: man, Source: abs, Integrity: pkg.Integrity,
			Root: abs, Kind: PackKindPath, held: true,
		}, nil
	case PackKindGit:
		root, err := CachedPackRevisionDir(pkg.ID, pkg.Revision)
		if err != nil {
			return packageCandidate{}, err
		}
		man, err := LoadManifest(root)
		if err != nil {
			return packageCandidate{}, err
		}
		candidate := packageCandidate{
			Manifest: man, Source: pkg.Source, Ref: pkg.Ref, Subdir: pkg.Subdir,
			Revision: pkg.Revision, Integrity: pkg.Integrity, Root: root,
			Kind: PackKindGit, held: true,
		}
		if err := verifyCachedCandidate(root, candidate); err != nil {
			return packageCandidate{}, err
		}
		return candidate, nil
	default:
		return packageCandidate{}, fmt.Errorf("unknown kind %q", pkg.Kind)
	}
}

func storeReleaseCandidate(candidate packageCandidate) error {
	if candidate.Kind == PackKindPath {
		return nil
	}
	target, err := CachedPackRevisionDir(candidate.Manifest.ID, candidate.Revision)
	if err != nil {
		return err
	}
	meta := PackageBodyMetadata{
		PackID: candidate.Manifest.ID, Version: candidate.Manifest.Version,
		ResolvedRevision: candidate.Revision, Integrity: candidate.Integrity,
		ExtensionAPI: candidate.Manifest.Compatibility.ExtensionAPI,
		Kind:         PackKindGit,
	}
	if _, err := os.Stat(target); err == nil {
		if err := verifyCachedCandidate(target, candidate); err != nil {
			return fmt.Errorf("cached %s@%s failed integrity verification", candidate.Manifest.ID, candidate.Manifest.Version)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	transactionDir, err := os.MkdirTemp(parent, ".candidate-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(transactionDir) }()
	staged := filepath.Join(transactionDir, "body")
	if err := copyDir(candidate.Root, staged); err != nil {
		return err
	}
	if err := WritePackageBodyMetadata(staged, meta); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		if _, statErr := os.Stat(target); statErr == nil {
			if verifyErr := verifyCachedCandidate(target, candidate); verifyErr == nil {
				return nil
			}
			return fmt.Errorf("concurrent cache publish for %s@%s failed integrity verification", candidate.Manifest.ID, candidate.Manifest.Version)
		}
		return err
	}
	return syncExtensionDirectory(parent)
}

func verifyCachedCandidate(target string, candidate packageCandidate) error {
	integrity, err := PackTreeIntegrity(target)
	if err != nil || integrity != candidate.Integrity {
		return fmt.Errorf("integrity mismatch")
	}
	meta, err := ReadPackageBodyMetadata(target)
	if err != nil {
		return err
	}
	if meta.PackID != candidate.Manifest.ID || meta.Version != candidate.Manifest.Version ||
		meta.ResolvedRevision != candidate.Revision || meta.Integrity != candidate.Integrity ||
		meta.ExtensionAPI != candidate.Manifest.Compatibility.ExtensionAPI || meta.Kind != PackKindGit {
		return fmt.Errorf("body metadata mismatch")
	}
	return nil
}

func candidateLockedPackage(candidate packageCandidate, graph map[string]packageCandidate) LockedPackage {
	dependencies := map[string]string{}
	for dependencyID := range candidate.Manifest.Dependencies {
		if dependency, ok := graph[dependencyID]; ok {
			dependencies[dependencyID] = dependency.Manifest.Version
			continue
		}
		if stock, ok := stockVersion(dependencyID); ok {
			dependencies[dependencyID] = stock
		}
	}
	return LockedPackage{
		ID: candidate.Manifest.ID, Version: candidate.Manifest.Version,
		Source: candidate.Source, Ref: candidate.Ref, Subdir: candidate.Subdir,
		Revision: candidate.Revision, Integrity: candidate.Integrity,
		Kind: candidate.Kind, Dependencies: dependencies,
	}
}

func stockVersion(id string) (string, bool) {
	stock, err := DiscoverStockContent()
	if err != nil {
		return "", false
	}
	for _, content := range stock {
		if content.Manifest.ID == id {
			return content.Manifest.Version, true
		}
	}
	return "", false
}

func retainReachablePackages(lock LockFile, desired DesiredState) LockFile {
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
		reachable[id] = true
		for dependencyID := range byID[id].Dependencies {
			if !IsStockPackID(dependencyID) {
				visit(dependencyID)
			}
		}
	}
	for _, root := range desired.Packs {
		visit(root.ID)
	}
	packages := make([]LockedPackage, 0, len(reachable))
	for _, pkg := range lock.Packages {
		if reachable[pkg.ID] {
			packages = append(packages, pkg)
		}
	}
	lock.Packages = packages
	return lock
}

func cleanupCandidates(candidates map[string]packageCandidate) {
	seen := map[string]bool{}
	for _, candidate := range candidates {
		root := strings.TrimSpace(candidate.Root)
		if candidate.Kind == PackKindGit && root != "" && !seen[root] {
			seen[root] = true
			candidate.cleanup()
		}
	}
}
