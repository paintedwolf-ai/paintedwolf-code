package extpacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitlease"
)

const maxMaterializedReleaseCandidates = 64

type packageCandidate struct {
	Manifest Manifest
	Source   string
	Ref      string
	// Subdir is where this package's manifest sits inside Source, empty for the
	// ordinary one-package-per-repository case.
	Subdir    string
	Revision  string
	Integrity string
	Root      string
	// staging is the tree to remove on cleanup. For a Subdir package Root points
	// inside the clone, so removing Root alone would leave the checkout behind.
	staging string
	Kind    PackKind
	// held marks a lock-seeded candidate whose Root is durable cache or a
	// linked path. Cleanup must not remove it.
	held bool
}

func (c packageCandidate) cleanup() {
	if c.held {
		return
	}
	target := c.staging
	if target == "" {
		target = c.Root
	}
	_ = os.RemoveAll(target)
}

type resolutionRequirement struct {
	ID          string
	Source      string
	Constraint  string
	RequestedBy string
}

type graphResolver struct {
	ctx context.Context
	// projectDir anchors relative sources for project installs.
	projectDir string
	candidates map[string][]packageCandidate
	errors     map[string][]error
	truncated  map[string]bool
}

func newGraphResolver(ctx context.Context, projectDir string) *graphResolver {
	return &graphResolver{
		ctx: ctx, projectDir: projectDir,
		candidates: map[string][]packageCandidate{}, errors: map[string][]error{}, truncated: map[string]bool{},
	}
}

func resolveReleaseGraph(ctx context.Context, source, constraint, projectDir string) (map[string]packageCandidate, string, error) {
	r := newGraphResolver(ctx, projectDir)
	source = strings.TrimSpace(source)
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		constraint = "*"
	}
	want, err := semver.NewConstraint(constraint)
	if err != nil {
		return nil, "", fmt.Errorf("version constraint: %w", err)
	}
	rootRequirement := resolutionRequirement{
		Source: source, Constraint: constraint, RequestedBy: "root",
	}
	rootCandidates, _, err := r.releaseCandidates(source, []resolutionRequirement{rootRequirement})
	if err != nil {
		return nil, "", err
	}
	rootID := ""
	for _, candidate := range rootCandidates {
		version, _ := parseCanonicalVersion(candidate.Manifest.Version)
		if !want.Check(version) {
			continue
		}
		if rootID == "" {
			rootID = candidate.Manifest.ID
		}
		if candidate.Manifest.ID != rootID {
			return nil, "", fmt.Errorf("source publishes multiple pack ids: %s and %s", rootID, candidate.Manifest.ID)
		}
	}
	if rootID == "" {
		return nil, "", fmt.Errorf("no release from %s satisfies %s and extension API %s", source, constraint, ExtensionAPIVersion)
	}
	requirements := map[string][]resolutionRequirement{
		rootID: {{ID: rootID, Source: source, Constraint: constraint, RequestedBy: "root"}},
	}
	selected, err := r.solve(requirements, map[string]packageCandidate{})
	if err != nil {
		r.cleanupCandidates()
		return nil, "", err
	}
	if err := rejectDependencyCycles(selected); err != nil {
		r.cleanupCandidates()
		return nil, "", err
	}
	r.cleanupUnselected(selected)
	return cloneCandidates(selected), rootID, nil
}

func resolveExactGraph(ctx context.Context, source, ref, projectDir string) (map[string]packageCandidate, string, error) {
	r := newGraphResolver(ctx, projectDir)
	root, err := r.exactCandidate(source, ref, "")
	if err != nil {
		return nil, "", err
	}
	requirements := requirementsForCandidate(root)
	selected := map[string]packageCandidate{root.Manifest.ID: root}
	selected, err = r.solve(requirements, selected)
	if err != nil {
		root.cleanup()
		r.cleanupCandidates()
		return nil, "", err
	}
	if err := rejectDependencyCycles(selected); err != nil {
		root.cleanup()
		r.cleanupCandidates()
		return nil, "", err
	}
	result := cloneCandidates(selected)
	r.cleanupUnselected(selected)
	return result, root.Manifest.ID, nil
}

func resolveDevelopmentGraph(ctx context.Context, root, projectDir string) (map[string]packageCandidate, string, error) {
	rootCandidate, err := developmentCandidate(root)
	if err != nil {
		return nil, "", err
	}
	r := newGraphResolver(ctx, projectDir)
	selected := map[string]packageCandidate{rootCandidate.Manifest.ID: rootCandidate}
	selected, err = r.solve(requirementsForCandidate(rootCandidate), selected)
	if err != nil {
		r.cleanupCandidates()
		return nil, "", err
	}
	if err := rejectDependencyCycles(selected); err != nil {
		r.cleanupCandidates()
		return nil, "", err
	}
	r.cleanupUnselected(selected)
	return selected, rootCandidate.Manifest.ID, nil
}

func developmentCandidate(root string) (packageCandidate, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return packageCandidate{}, err
	}
	man, err := LoadManifest(abs)
	if err != nil {
		return packageCandidate{}, err
	}
	if !ManifestHostCompatible(man) {
		return packageCandidate{}, fmt.Errorf("pack requires extension API %s; host provides %s", man.Compatibility.ExtensionAPI, ExtensionAPIVersion)
	}
	if _, err := InventoryPack(Pack{ID: man.ID, Root: OnDisk(abs)}, man); err != nil {
		return packageCandidate{}, err
	}
	integrity, err := PackTreeIntegrity(abs)
	if err != nil {
		return packageCandidate{}, err
	}
	return packageCandidate{Manifest: man, Source: abs, Integrity: integrity, Root: abs, Kind: PackKindPath}, nil
}

// Seeded suite packages participate in source and version constraints.
func resolveIntentGraph(
	ctx context.Context,
	desired DesiredState,
	projectDir string,
	seeded map[string]packageCandidate,
) (map[string]packageCandidate, []DesiredPack, error) {
	r := newGraphResolver(ctx, projectDir)
	selected := cloneCandidates(seeded)
	seedIDs := make(map[string]bool, len(seeded))
	for id := range seeded {
		seedIDs[id] = true
	}
	requirements := map[string][]resolutionRequirement{}
	var roots []DesiredPack
	fail := func(err error) (map[string]packageCandidate, []DesiredPack, error) {
		for id, candidate := range selected {
			if candidate.Kind == PackKindGit && !seedIDs[id] {
				candidate.cleanup()
			}
		}
		r.cleanupCandidates()
		return nil, nil, err
	}
	for _, row := range desired.Packs {
		if strings.TrimSpace(row.Source) == "" {
			continue
		}
		roots = append(roots, row)
		if candidate, fixed := selected[row.ID]; fixed && seedIDs[row.ID] {
			if err := validateSeededIntent(row, candidate, projectDir); err != nil {
				return fail(fmt.Errorf("lock %s: %w", row.ID, err))
			}
			var mergeErr error
			requirements, mergeErr = mergeRequirements(requirements, candidate, projectDir)
			if mergeErr != nil {
				return fail(mergeErr)
			}
			if !row.Development && row.Ref == "" {
				requirements[row.ID] = append(requirements[row.ID], resolutionRequirement{
					ID: row.ID, Source: candidate.Source, Constraint: row.Version, RequestedBy: "desired root",
				})
			}
			continue
		}
		switch {
		case row.Development:
			abs, linked, err := resolveLinkedLocalSource(row.Source, projectDir)
			if err != nil {
				return fail(fmt.Errorf("lock %s: development source: %w", row.ID, err))
			}
			if !linked {
				return fail(fmt.Errorf("lock %s: development source must be a linked local directory", row.ID))
			}
			candidate, err := developmentCandidate(abs)
			if err != nil {
				return fail(fmt.Errorf("lock %s: %w", row.ID, err))
			}
			if candidate.Manifest.ID != row.ID {
				return fail(fmt.Errorf("lock source declares %s, expected %s", candidate.Manifest.ID, row.ID))
			}
			selected[row.ID] = candidate
			var mergeErr error
			requirements, mergeErr = mergeRequirements(requirements, candidate, projectDir)
			if mergeErr != nil {
				return fail(mergeErr)
			}
		case row.Ref != "":
			candidate, err := r.exactCandidate(row.Source, row.Ref, row.Subdir)
			if err != nil {
				return fail(fmt.Errorf("lock %s: %w", row.ID, err))
			}
			if candidate.Manifest.ID != row.ID {
				candidate.cleanup()
				return fail(fmt.Errorf("lock source declares %s, expected %s", candidate.Manifest.ID, row.ID))
			}
			selected[row.ID] = candidate
			var mergeErr error
			requirements, mergeErr = mergeRequirements(requirements, candidate, projectDir)
			if mergeErr != nil {
				return fail(mergeErr)
			}
		default:
			requirement := resolutionRequirement{
				ID: row.ID, Source: row.Source, Constraint: row.Version, RequestedBy: "desired root",
			}
			candidates, _, err := r.releaseCandidates(row.Source, []resolutionRequirement{requirement})
			if err != nil {
				return fail(fmt.Errorf("lock %s: %w", row.ID, err))
			}
			foundID := false
			for _, candidate := range candidates {
				if candidate.Manifest.ID == row.ID {
					foundID = true
					break
				}
			}
			if !foundID {
				return fail(fmt.Errorf("lock source has no release for %s", row.ID))
			}
			requirements[row.ID] = append(requirements[row.ID], requirement)
		}
	}
	resolved, err := r.solve(requirements, selected)
	if err != nil {
		return fail(err)
	}
	if err := rejectDependencyCycles(resolved); err != nil {
		return fail(err)
	}
	r.cleanupUnselected(resolved)
	return resolved, roots, nil
}

func validateSeededIntent(row DesiredPack, candidate packageCandidate, projectDir string) error {
	if candidate.Manifest.ID != row.ID {
		return fmt.Errorf("fixed candidate declares %s", candidate.Manifest.ID)
	}
	switch {
	case row.Development:
		abs, linked, err := resolveLinkedLocalSource(row.Source, projectDir)
		if err != nil {
			return err
		}
		if !linked || candidate.Kind != PackKindPath || filepath.Clean(abs) != filepath.Clean(candidate.Root) {
			return fmt.Errorf("fixed candidate does not match development source %s", row.Source)
		}
	case row.Ref != "":
		if candidate.Kind != PackKindGit || candidate.Source != row.Source ||
			candidate.Ref != row.Ref || candidate.Subdir != row.Subdir {
			return fmt.Errorf("fixed candidate does not match %s at ref %s", row.Source, row.Ref)
		}
	default:
		if row.Subdir != "" {
			// Only a member of a multi-package source carries one, and those pin
			// the source's exact ref.
			return fmt.Errorf("subdir requires an exact ref")
		}
		if candidate.Kind != PackKindGit || candidate.Source != row.Source {
			return fmt.Errorf("fixed candidate does not match release source %s", row.Source)
		}
		if !dependencyVersionSatisfied(DependencyRequest{Version: row.Version}, candidate.Manifest.Version) {
			return fmt.Errorf("fixed candidate version %s does not satisfy %s", candidate.Manifest.Version, row.Version)
		}
	}
	return nil
}

// Suite members share one dependency decision.
func resolveLocalRootsGraph(
	ctx context.Context,
	roots map[string]string,
	kind PackKind,
	inspectRoot, source, ref, revision, projectDir string,
) (map[string]packageCandidate, error) {
	selected := make(map[string]packageCandidate, len(roots))
	requirements := map[string][]resolutionRequirement{}
	for expectedID, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		man, err := LoadManifest(abs)
		if err != nil {
			return nil, err
		}
		if man.ID != expectedID {
			return nil, fmt.Errorf("suite member %s declares %s", expectedID, man.ID)
		}
		if !ManifestHostCompatible(man) {
			return nil, fmt.Errorf("pack %s requires extension API %s; host provides %s", man.ID, man.Compatibility.ExtensionAPI, ExtensionAPIVersion)
		}
		if _, err := InventoryPack(Pack{ID: man.ID, Root: OnDisk(abs)}, man); err != nil {
			return nil, err
		}
		integrity, err := PackTreeIntegrity(abs)
		if err != nil {
			return nil, err
		}
		candidateSource := abs
		subdir := ""
		if kind == PackKindGit {
			candidateSource = source
			// Carry the member's directory so the lock can find it again.
			if rel, relErr := filepath.Rel(inspectRoot, abs); relErr == nil && rel != "." {
				subdir = filepath.ToSlash(rel)
			}
		}
		candidate := packageCandidate{
			Manifest: man, Source: candidateSource, Ref: ref, Subdir: subdir, Revision: revision,
			Integrity: integrity, Root: abs, Kind: kind,
		}
		selected[man.ID] = candidate
		for id, reqs := range requirementsForCandidate(candidate) {
			requirements[id] = append(requirements[id], reqs...)
		}
	}
	r := newGraphResolver(ctx, projectDir)
	resolved, err := r.solve(requirements, selected)
	if err != nil {
		r.cleanupCandidates()
		return nil, err
	}
	if err := rejectDependencyCycles(resolved); err != nil {
		r.cleanupCandidates()
		return nil, err
	}
	r.cleanupUnselected(resolved)
	return resolved, nil
}

func (r *graphResolver) solve(
	requirements map[string][]resolutionRequirement,
	selected map[string]packageCandidate,
) (map[string]packageCandidate, error) {
	for id, reqs := range requirements {
		if candidate, ok := selected[id]; ok {
			if err := candidateSatisfies(candidate, reqs, r.projectDir); err != nil {
				return nil, err
			}
		}
	}
	next := ""
	for id := range requirements {
		if _, ok := selected[id]; !ok && !IsStockPackID(id) {
			if next == "" || id < next {
				next = id
			}
		}
	}
	if next == "" {
		if err := validateStockRequirements(requirements, r.projectDir); err != nil {
			return nil, err
		}
		return selected, nil
	}
	reqs := requirements[next]
	source, err := oneRequirementSource(next, reqs, r.projectDir)
	if err != nil {
		return nil, err
	}
	var candidates []packageCandidate
	var truncated bool
	if abs, linked, linkErr := resolveLinkedLocalSource(source, r.projectDir); linkErr != nil {
		return nil, fmt.Errorf("dependency %s: %w", next, linkErr)
	} else if linked {
		candidate, devErr := developmentCandidate(abs)
		if devErr != nil {
			return nil, fmt.Errorf("dependency %s: %w", next, devErr)
		}
		// Linked directories belong to the author and are excluded from cleanup.
		candidate.held = true
		candidates = []packageCandidate{candidate}
	} else {
		candidates, truncated, err = r.releaseCandidates(source, reqs)
		if err != nil {
			return nil, err
		}
	}
	var failures []error
	for _, candidate := range candidates {
		if candidate.Manifest.ID != next {
			continue
		}
		if err := candidateSatisfies(candidate, reqs, r.projectDir); err != nil {
			continue
		}
		nextSelected := cloneCandidates(selected)
		nextSelected[next] = candidate
		nextRequirements, err := mergeRequirements(requirements, candidate, r.projectDir)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		resolved, err := r.solve(nextRequirements, nextSelected)
		if err == nil {
			return resolved, nil
		}
		failures = append(failures, err)
	}
	if truncated {
		failures = append(failures, fmt.Errorf(
			"more than %d releases satisfy %s; use a narrower constraint",
			maxMaterializedReleaseCandidates, describeRequirements(reqs),
		))
	}
	if len(failures) > 0 {
		return nil, fmt.Errorf("cannot resolve %s: %w", next, errors.Join(failures...))
	}
	return nil, fmt.Errorf("no compatible release of %s satisfies %s", next, describeRequirements(reqs))
}

func (r *graphResolver) releaseCandidates(
	source string,
	requirements []resolutionRequirement,
) ([]packageCandidate, bool, error) {
	key := releaseCandidateKey(source, requirements)
	if candidates, ok := r.candidates[key]; ok {
		return candidates, r.truncated[key], nil
	}
	refs, err := releaseRefs(r.ctx, source)
	if err != nil {
		return nil, false, err
	}
	matchingRefs := make([]string, 0, len(refs))
	for _, ref := range refs {
		version, err := parseCanonicalVersion(strings.TrimPrefix(ref, "v"))
		if err != nil || !requirementsAdmitVersion(requirements, version) {
			continue
		}
		matchingRefs = append(matchingRefs, ref)
	}
	if len(matchingRefs) > maxMaterializedReleaseCandidates {
		r.truncated[key] = true
		matchingRefs = matchingRefs[:maxMaterializedReleaseCandidates]
	}
	var candidates []packageCandidate
	for _, ref := range matchingRefs {
		candidate, err := r.exactCandidate(source, ref, "")
		if err != nil {
			r.errors[key] = append(r.errors[key], fmt.Errorf("%s: %w", ref, err))
			continue
		}
		if !ManifestHostCompatible(candidate.Manifest) {
			candidate.cleanup()
			continue
		}
		if "v"+candidate.Manifest.Version != ref && candidate.Manifest.Version != ref {
			candidate.cleanup()
			r.errors[key] = append(r.errors[key], fmt.Errorf("tag %s contains version %s", ref, candidate.Manifest.Version))
			continue
		}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, _ := parseCanonicalVersion(candidates[i].Manifest.Version)
		right, _ := parseCanonicalVersion(candidates[j].Manifest.Version)
		if left.Equal(right) {
			return candidates[i].Ref < candidates[j].Ref
		}
		return left.GreaterThan(right)
	})
	r.candidates[key] = candidates
	if len(candidates) == 0 {
		if r.truncated[key] {
			return nil, true, fmt.Errorf(
				"more than %d releases satisfy %s; use a narrower constraint",
				maxMaterializedReleaseCandidates, describeRequirements(requirements),
			)
		}
		if errs := r.errors[key]; len(errs) > 0 {
			return nil, r.truncated[key], fmt.Errorf("source %s has no valid compatible releases: %w", source, errors.Join(errs...))
		}
		return nil, r.truncated[key], fmt.Errorf("source %s has no release satisfying %s", source, describeRequirements(requirements))
	}
	return candidates, r.truncated[key], nil
}

func releaseCandidateKey(source string, requirements []resolutionRequirement) string {
	constraints := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		constraints = append(constraints, strings.TrimSpace(requirement.Constraint))
	}
	sort.Strings(constraints)
	return source + "\x00" + strings.Join(constraints, "\x00")
}

func requirementsAdmitVersion(requirements []resolutionRequirement, version *semver.Version) bool {
	for _, requirement := range requirements {
		constraint, err := semver.NewConstraint(requirement.Constraint)
		if err != nil || !constraint.Check(version) {
			return false
		}
	}
	return true
}

func (r *graphResolver) exactCandidate(source, ref, subdir string) (packageCandidate, error) {
	clone, err := materializeGitSource(r.ctx, source, ref)
	if err != nil {
		return packageCandidate{}, err
	}
	fail := func(err error) (packageCandidate, error) {
		_ = os.RemoveAll(clone)
		return packageCandidate{}, err
	}
	root := clone
	if subdir != "" {
		root, err = packageSubdirRoot(clone, subdir)
		if err != nil {
			return fail(err)
		}
	}
	man, err := LoadManifest(root)
	if err != nil {
		return fail(err)
	}
	if !ManifestHostCompatible(man) {
		return fail(fmt.Errorf("pack requires extension API %s; host provides %s", man.Compatibility.ExtensionAPI, ExtensionAPIVersion))
	}
	if _, err := InventoryPack(Pack{ID: man.ID, Root: OnDisk(root)}, man); err != nil {
		return fail(err)
	}
	revision, err := gitRevParse(r.ctx, clone, "HEAD")
	if err != nil {
		return fail(err)
	}
	integrity, err := PackTreeIntegrity(root)
	if err != nil {
		return fail(err)
	}
	return packageCandidate{
		Manifest: man, Source: source, Ref: ref, Subdir: subdir,
		Revision: revision, Integrity: integrity, Root: root, staging: clone, Kind: PackKindGit,
	}, nil
}

// packageSubdirRoot resolves a member directory inside a materialized source and
// refuses anything that leaves it.
func packageSubdirRoot(clone, subdir string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(subdir)))
	if clean == "" || clean == "." || filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("subdir %q must be a relative path inside the source", subdir)
	}
	root := filepath.Join(clone, clean)
	if _, err := os.Stat(filepath.Join(root, config.PackManifestName)); err != nil {
		return "", fmt.Errorf("subdir %q has no %s", subdir, config.PackManifestName)
	}
	return root, nil
}

func releaseRefs(ctx context.Context, source string) ([]string, error) {
	if err := gitargv.ValidateCloneURL(source); err != nil {
		return nil, err
	}
	// Version resolution reads the same refs a clone would; against a local
	// source that read races this host's own writes exactly the same way.
	releaseSource, err := gitlease.CloneSource(ctx, source)
	if err != nil {
		return nil, err
	}
	defer releaseSource()
	networkCtx, cancel := gitNetworkContext(ctx)
	defer cancel()
	out, code, err := gitexec.Run(networkCtx, os.TempDir(), []string{"ls-remote", "--tags", "--refs", "--", source}, gitexec.Opts{
		Profile: gitexec.ProfileNetwork, RemoteURL: source, Timeout: 10 * time.Minute,
	})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git ls-remote: exit %d: %s", code, strings.TrimSpace(string(out)))
	}
	type releaseRef struct {
		name    string
		version *semver.Version
	}
	var releases []releaseRef
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "refs/tags/")
		versionText := strings.TrimPrefix(name, "v")
		version, err := parseCanonicalVersion(versionText)
		if err != nil {
			continue
		}
		releases = append(releases, releaseRef{name: name, version: version})
	}
	sort.Slice(releases, func(i, j int) bool {
		if releases[i].version.Equal(releases[j].version) {
			return releases[i].name < releases[j].name
		}
		return releases[i].version.GreaterThan(releases[j].version)
	})
	refs := make([]string, len(releases))
	for i := range releases {
		refs[i] = releases[i].name
	}
	return refs, nil
}

func requirementsForCandidate(candidate packageCandidate) map[string][]resolutionRequirement {
	requirements := map[string][]resolutionRequirement{}
	for id, dependency := range candidate.Manifest.Dependencies {
		requirements[id] = append(requirements[id], resolutionRequirement{
			ID: id, Source: dependency.Source, Constraint: dependency.Version, RequestedBy: candidate.Manifest.ID,
		})
	}
	return requirements
}

func mergeRequirements(current map[string][]resolutionRequirement, candidate packageCandidate, projectDir string) (map[string][]resolutionRequirement, error) {
	next := make(map[string][]resolutionRequirement, len(current)+len(candidate.Manifest.Dependencies))
	for id, reqs := range current {
		next[id] = append([]resolutionRequirement(nil), reqs...)
	}
	for id, dependency := range candidate.Manifest.Dependencies {
		req := resolutionRequirement{ID: id, Source: dependency.Source, Constraint: dependency.Version, RequestedBy: candidate.Manifest.ID}
		next[id] = append(next[id], req)
		if _, err := oneRequirementSource(id, next[id], projectDir); err != nil {
			return nil, err
		}
	}
	return next, nil
}

// normalizeRequirementSource makes local paths absolute for source comparison.
func normalizeRequirementSource(source, projectDir string) string {
	if abs, linked, err := resolveLinkedLocalSource(source, projectDir); err == nil && linked {
		return abs
	}
	return source
}

func oneRequirementSource(id string, reqs []resolutionRequirement, projectDir string) (string, error) {
	source := ""
	normalized := ""
	for _, req := range reqs {
		if IsStockPackID(id) && strings.TrimSpace(req.Source) == "" {
			continue
		}
		reqNormalized := normalizeRequirementSource(req.Source, projectDir)
		if source == "" {
			source = req.Source
			normalized = reqNormalized
			continue
		}
		if normalized != reqNormalized {
			return "", fmt.Errorf("dependency %s has conflicting sources %q and %q", id, source, req.Source)
		}
	}
	if source == "" && !IsStockPackID(id) {
		return "", fmt.Errorf("dependency %s has no source", id)
	}
	return source, nil
}

func candidateSatisfies(candidate packageCandidate, reqs []resolutionRequirement, projectDir string) error {
	version, _ := parseCanonicalVersion(candidate.Manifest.Version)
	for _, req := range reqs {
		if !IsStockPackID(req.ID) && strings.TrimSpace(req.Source) != "" &&
			normalizeRequirementSource(req.Source, projectDir) != candidate.Source {
			return fmt.Errorf("%s requires %s from %s, selected source is %s", req.RequestedBy, req.ID, req.Source, candidate.Source)
		}
		constraint, err := semver.NewConstraint(req.Constraint)
		if err != nil {
			return fmt.Errorf("%s requires %s %s: %w", req.RequestedBy, req.ID, req.Constraint, err)
		}
		if !constraint.Check(version) {
			return fmt.Errorf("%s requires %s %s, selected %s", req.RequestedBy, req.ID, req.Constraint, candidate.Manifest.Version)
		}
	}
	return nil
}

func validateStockRequirements(requirements map[string][]resolutionRequirement, projectDir string) error {
	stock, err := DiscoverStockContent()
	if err != nil {
		return err
	}
	byID := map[string]packageCandidate{}
	for _, content := range stock {
		byID[content.Manifest.ID] = packageCandidate{Manifest: content.Manifest}
	}
	for id, reqs := range requirements {
		if !IsStockPackID(id) {
			continue
		}
		candidate, ok := byID[id]
		if !ok {
			return fmt.Errorf("required stock package %s is unavailable", id)
		}
		if err := candidateSatisfies(candidate, reqs, projectDir); err != nil {
			return err
		}
	}
	return nil
}

func rejectDependencyCycles(selected map[string]packageCandidate) error {
	state := map[string]uint8{}
	var visit func(string, []string) error
	visit = func(id string, stack []string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("dependency cycle: %s", strings.Join(append(stack, id), " -> "))
		case 2:
			return nil
		}
		state[id] = 1
		candidate := selected[id]
		for dependencyID := range candidate.Manifest.Dependencies {
			if IsStockPackID(dependencyID) {
				continue
			}
			if err := visit(dependencyID, append(stack, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range selected {
		if err := visit(id, nil); err != nil {
			return err
		}
	}
	return nil
}

func describeRequirements(reqs []resolutionRequirement) string {
	parts := make([]string, 0, len(reqs))
	for _, req := range reqs {
		parts = append(parts, fmt.Sprintf("%s from %s", req.Constraint, req.RequestedBy))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func cloneCandidates(in map[string]packageCandidate) map[string]packageCandidate {
	out := make(map[string]packageCandidate, len(in))
	for id, candidate := range in {
		out[id] = candidate
	}
	return out
}

func (r *graphResolver) cleanupCandidates() {
	for _, candidates := range r.candidates {
		for _, candidate := range candidates {
			candidate.cleanup()
		}
	}
}

func (r *graphResolver) cleanupUnselected(selected map[string]packageCandidate) {
	keep := map[string]struct{}{}
	for _, candidate := range selected {
		keep[candidate.Root] = struct{}{}
	}
	for _, candidates := range r.candidates {
		for _, candidate := range candidates {
			if _, ok := keep[candidate.Root]; !ok {
				candidate.cleanup()
			}
		}
	}
}
