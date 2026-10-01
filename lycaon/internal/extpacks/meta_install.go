package extpacks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// InstallMetaOptions configures suite installation.
type InstallMetaOptions struct {
	Source     string // https://…, file:///…, path:rel-or-abs
	Version    string // SemVer constraint; default * for released Git suites
	Ref        string
	ProjectDir string
}

// InstallMetaResult describes a prepared suite installation.
type InstallMetaResult struct {
	MetaPackID     string
	MetaRoot       string
	MemberPackIDs  []string
	NewlyInstalled []string
	DesiredPath    string
	Warnings       []string
	Version        string
}

// ErrUseInstallMeta identifies a suite passed to leaf installation.
var ErrUseInstallMeta = fmt.Errorf("source is a meta-pack; use install-meta")

// ErrAmbiguousPackRoot rejects roots that ship both manifests.
var ErrAmbiguousPackRoot = fmt.Errorf("root has both meta.yaml and extension.yaml")

// PrepareInstallMeta resolves every suite member into one package plan.
func PrepareInstallMeta(ctx context.Context, opts InstallMetaOptions) (*PackagePlan, error) {
	source := strings.TrimSpace(opts.Source)
	if source == "" {
		return nil, fmt.Errorf("install-meta: source required")
	}
	ref := strings.TrimSpace(opts.Ref)
	version := strings.TrimSpace(opts.Version)
	if ref != "" && version != "" {
		return nil, fmt.Errorf("install-meta: version and ref are mutually exclusive")
	}
	staging, kind, authorRoot, selectedRef, err := materializeSelectedMetaSource(ctx, source, version, ref, opts.ProjectDir)
	if err != nil {
		return nil, err
	}
	closeStaging := func() {
		if staging != "" && kind == PackKindGit {
			_ = os.RemoveAll(staging)
		}
	}
	if kind == PackKindPath {
		if err := refuseLinkIntoManagedExtensionCaches(authorRoot); err != nil {
			closeStaging()
			return nil, err
		}
	}
	ref = selectedRef

	inspectRoot := staging
	if inspectRoot == "" {
		inspectRoot = authorRoot
	}
	if err := assertMetaOnlyRoot(inspectRoot); err != nil {
		closeStaging()
		return nil, err
	}

	data, err := os.ReadFile(filepath.Join(inspectRoot, MetaPackFileName))
	if err != nil {
		closeStaging()
		return nil, fmt.Errorf("install-meta: read meta.yaml: %w", err)
	}
	man, diags := parseMetaPackManifest(data, MetaPackFileName)
	if len(diags) > 0 {
		closeStaging()
		return nil, fmt.Errorf("install-meta: %s", diags[0].Message)
	}
	if IsStockMetaPackID(man.ID) {
		closeStaging()
		return nil, fmt.Errorf("install-meta: refusing stock id: %w", ErrStockMetaPack)
	}

	revision := ""
	if kind == PackKindGit {
		revision, err = gitRevParse(ctx, inspectRoot, "HEAD")
		if err != nil {
			closeStaging()
			return nil, err
		}
	}

	graph, direct, newly, err := resolveSuiteMembers(ctx, suiteMemberInput{
		InspectRoot: inspectRoot, Members: man.Members,
		ProjectDir: opts.ProjectDir, Kind: kind, Source: source, Ref: ref, Revision: revision,
	})
	if err != nil {
		closeStaging()
		return nil, err
	}
	subjects := make([]string, 0, len(direct))
	for _, row := range direct {
		subjects = append(subjects, row.ID)
	}
	roots, scopeGraph, err := resolveDeviceGraph(ctx, opts.ProjectDir, direct, graph, subjects)
	if err != nil {
		cleanupCandidates(graph)
		closeStaging()
		return nil, fmt.Errorf("install-meta: resolve scope: %w", err)
	}
	if err := storePlanCandidates(scopeGraph); err != nil {
		cleanupCandidates(graph)
		cleanupCandidates(scopeGraph)
		closeStaging()
		return nil, err
	}

	metaDir, err := CachedMetaPackDir(man.ID)
	if err != nil {
		cleanupCandidates(graph)
		cleanupCandidates(scopeGraph)
		closeStaging()
		return nil, err
	}
	metaSource := source
	if kind == PackKindPath {
		metaSource = authorRoot
	}
	meta := MetaPackMetadata{
		MetaPackID: man.ID, Version: man.Version, Source: metaSource, Ref: ref,
		ResolvedRevision: revision, ExtensionAPI: man.Compatibility.ExtensionAPI,
		MemberPackIDs: append([]string(nil), man.Members...), Kind: kind,
	}
	cacheRoot, err := prepareMetaRootTransaction(inspectRoot, metaDir, kind, meta)
	if err != nil {
		cleanupCandidates(graph)
		cleanupCandidates(scopeGraph)
		closeStaging()
		return nil, err
	}
	plan := &PackagePlan{
		Roots: roots,
		Meta: &InstallMetaResult{
			MetaPackID:     man.ID,
			MetaRoot:       metaDir,
			MemberPackIDs:  append([]string(nil), man.Members...),
			NewlyInstalled: newly,
			Version:        man.Version,
		},
		graph:     scopeGraph,
		cleanups:  []map[string]packageCandidate{graph},
		cacheRoot: cacheRoot,
	}
	if staging != "" && kind == PackKindGit {
		plan.stagingDir = staging
	}
	return plan, nil
}

type suiteMemberInput struct {
	InspectRoot string
	Members     []string
	ProjectDir  string
	Kind        PackKind
	Source      string
	Ref         string
	Revision    string
}

// resolveSuiteMembers builds the suite member graph and desired roots.
func resolveSuiteMembers(ctx context.Context, in suiteMemberInput) (map[string]packageCandidate, []DesiredPack, []string, error) {
	inventory, err := inventoryPackIDSet()
	if err != nil {
		return nil, nil, nil, err
	}
	memberRoots := map[string]string{}
	for _, memberID := range in.Members {
		if leaf, ok := findMemberLeafDir(in.InspectRoot, memberID); ok {
			if IsStockPackID(memberID) {
				return nil, nil, nil, fmt.Errorf("install-meta: suite cannot ship stock package id %q", memberID)
			}
			memberRoots[memberID] = leaf
			continue
		}
		if !inventory[memberID] {
			return nil, nil, nil, fmt.Errorf("install-meta: member %q missing as subdir and not in inventory", memberID)
		}
	}
	graph, err := resolveLocalRootsGraph(ctx, memberRoots, in.Kind, in.InspectRoot, in.Source, in.Ref, in.Revision, in.ProjectDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("install-meta: resolve members: %w", err)
	}
	direct := make([]DesiredPack, 0, len(memberRoots))
	newly := make([]string, 0, len(memberRoots))
	for _, memberID := range in.Members {
		leaf, ok := memberRoots[memberID]
		if !ok {
			continue
		}
		row := DesiredPack{ID: memberID, Source: in.Source, Ref: in.Ref, Enabled: boolPtr(true)}
		if in.Kind == PackKindPath {
			row.Source = "path:" + leaf
			row.Ref = ""
			row.Development = true
		} else if rel, err := filepath.Rel(in.InspectRoot, leaf); err == nil && rel != "." {
			// Record where inside the suite source this member's manifest sits,
			// so a later re-resolution of the row can find it.
			row.Subdir = filepath.ToSlash(rel)
		}
		direct = append(direct, row)
		newly = append(newly, memberID)
	}
	return graph, direct, newly, nil
}

// RemoveMetaPack removes suite metadata without changing member selection.
func RemoveMetaPack(metaID string) error {
	metaID = strings.TrimSpace(metaID)
	if metaID == "" {
		return fmt.Errorf("remove-meta: meta-pack id required")
	}
	if IsStockMetaPackID(metaID) {
		return fmt.Errorf("remove-meta: cannot remove %q: %w", metaID, ErrStockMetaPack)
	}
	if err := ValidatePackID(metaID); err != nil {
		return fmt.Errorf("remove-meta: %w", err)
	}
	metaDir, err := CachedMetaPackDir(metaID)
	if err != nil {
		return err
	}
	unlock := lockExtensionPath(metaDir)
	defer unlock()
	releaseFile, err := lockExtensionPathAcrossProcesses(metaDir)
	if err != nil {
		return err
	}
	defer releaseFile()
	if _, err := os.Stat(metaDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	transaction, err := os.MkdirTemp(filepath.Dir(metaDir), metaRemovalPrefix)
	if err != nil {
		return err
	}
	tombstone := filepath.Join(transaction, "removed")
	if err := os.Rename(metaDir, tombstone); err != nil {
		_ = os.RemoveAll(transaction)
		return err
	}
	if err := syncExtensionDirectory(filepath.Dir(metaDir)); err != nil {
		if rollbackErr := os.Rename(tombstone, metaDir); rollbackErr == nil {
			_ = syncExtensionDirectory(filepath.Dir(metaDir))
		}
		_ = os.RemoveAll(transaction)
		return err
	}
	if err := os.RemoveAll(transaction); err != nil {
		return err
	}
	return syncExtensionDirectory(filepath.Dir(metaDir))
}

func materializeSelectedMetaSource(
	ctx context.Context,
	source, version, ref, projectDir string,
) (staging string, kind PackKind, authorRoot, selectedRef string, err error) {
	if abs, linked, linkErr := resolveLinkedLocalSource(source, projectDir); linkErr != nil {
		return "", "", "", "", linkErr
	} else if linked {
		if version != "" || ref != "" {
			return "", "", "", "", fmt.Errorf("install-meta: linked path sources do not accept version or ref")
		}
		return "", PackKindPath, abs, "", nil
	}
	if ref != "" {
		staging, kind, authorRoot, err = materializeMetaSource(ctx, source, ref, projectDir)
		return staging, kind, authorRoot, ref, err
	}
	if version == "" {
		version = "*"
	}
	want, err := semver.NewConstraint(version)
	if err != nil {
		return "", "", "", "", fmt.Errorf("install-meta: version constraint: %w", err)
	}
	refs, err := releaseRefs(ctx, source)
	if err != nil {
		return "", "", "", "", err
	}
	matchingRefs, truncated := matchingMetaReleaseRefs(refs, want)
	for _, releaseRef := range matchingRefs {
		candidate, candidateKind, _, cloneErr := materializeMetaSource(ctx, source, releaseRef, projectDir)
		if cloneErr != nil {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(candidate, MetaPackFileName))
		man, diags := parseMetaPackManifest(data, filepath.Join(candidate, MetaPackFileName))
		parsed, parseErr := parseCanonicalVersion(man.Version)
		if readErr == nil && len(diags) == 0 && parseErr == nil && want.Check(parsed) &&
			(releaseRef == man.Version || releaseRef == "v"+man.Version) {
			return candidate, candidateKind, "", releaseRef, nil
		}
		_ = os.RemoveAll(candidate)
	}
	if truncated {
		return "", "", "", "", fmt.Errorf(
			"install-meta: more than %d suite releases satisfy %s; use a narrower constraint",
			maxMaterializedReleaseCandidates, version,
		)
	}
	return "", "", "", "", fmt.Errorf("install-meta: no release from %s satisfies %s and extension API %s", source, version, ExtensionAPIVersion)
}

func matchingMetaReleaseRefs(refs []string, want *semver.Constraints) ([]string, bool) {
	matching := make([]string, 0, len(refs))
	for _, ref := range refs {
		version, err := parseCanonicalVersion(strings.TrimPrefix(ref, "v"))
		if err == nil && want.Check(version) {
			matching = append(matching, ref)
		}
	}
	if len(matching) <= maxMaterializedReleaseCandidates {
		return matching, false
	}
	return matching[:maxMaterializedReleaseCandidates], true
}

func prepareMetaRootTransaction(sourceRoot, target string, kind PackKind, meta MetaPackMetadata) (*metaRootTransaction, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, err
	}
	transaction, err := os.MkdirTemp(filepath.Dir(target), metaTransactionPrefix)
	if err != nil {
		return nil, err
	}
	staged := filepath.Join(transaction, "body")
	fail := func(cause error) (*metaRootTransaction, error) {
		_ = os.RemoveAll(transaction)
		return nil, cause
	}
	if kind == PackKindGit {
		if err := copyDir(sourceRoot, staged); err != nil {
			return fail(fmt.Errorf("install-meta: stage suite: %w", err))
		}
	} else {
		if err := os.MkdirAll(staged, 0o700); err != nil {
			return fail(err)
		}
		if err := copyFile(filepath.Join(sourceRoot, MetaPackFileName), filepath.Join(staged, MetaPackFileName)); err != nil {
			return fail(err)
		}
	}
	meta.Integrity, err = PackTreeIntegrity(staged)
	if err != nil {
		return fail(err)
	}
	if err := WriteMetaPackMetadata(staged, meta); err != nil {
		return fail(err)
	}
	return &metaRootTransaction{
		Format:    metaTransactionFormat,
		Directory: transaction,
		Staged:    staged,
		Target:    target,
		Backup:    filepath.Join(transaction, "previous"),
	}, nil
}

func materializeMetaSource(ctx context.Context, source, ref, projectDir string) (staging string, kind PackKind, authorRoot string, err error) {
	switch {
	case strings.HasPrefix(source, "path:"):
		rel := strings.TrimSpace(strings.TrimPrefix(source, "path:"))
		srcPath := rel
		if !filepath.IsAbs(srcPath) {
			base := projectDir
			if base == "" {
				base, _ = os.Getwd()
			}
			srcPath = filepath.Join(base, rel)
		}
		abs, err := filepath.Abs(srcPath)
		if err != nil {
			return "", "", "", err
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return "", "", "", fmt.Errorf("path source %q: directory required", abs)
		}
		return "", PackKindPath, abs, nil

	case strings.HasPrefix(source, "file://"):
		p, err := fileSourcePath(source)
		if err != nil {
			return "", "", "", err
		}
		info, err := os.Stat(p)
		if err != nil {
			return "", "", "", fmt.Errorf("file source: %w", err)
		}
		if !info.IsDir() {
			return "", "", "", fmt.Errorf("file source must be a directory")
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", "", "", err
		}
		if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			staging, err = os.MkdirTemp("", "lycaon-meta-*")
			if err != nil {
				return "", "", "", err
			}
			if err := gitClone(ctx, abs, staging, ref); err != nil {
				_ = os.RemoveAll(staging)
				return "", "", "", err
			}
			return staging, PackKindGit, "", nil
		}
		return "", PackKindPath, abs, nil

	default:
		staging, err = os.MkdirTemp("", "lycaon-meta-*")
		if err != nil {
			return "", "", "", err
		}
		if err := gitClone(ctx, source, staging, ref); err != nil {
			_ = os.RemoveAll(staging)
			return "", "", "", err
		}
		return staging, PackKindGit, "", nil
	}
}

func assertMetaOnlyRoot(root string) error {
	hasExt := fileExists(filepath.Join(root, "extension.yaml"))
	hasMeta := fileExists(filepath.Join(root, MetaPackFileName))
	switch {
	case hasMeta && hasExt:
		return ErrAmbiguousPackRoot
	case hasExt && !hasMeta:
		return fmt.Errorf("install-meta: root has extension.yaml only — use install")
	case !hasMeta:
		return fmt.Errorf("install-meta: meta.yaml required at source root")
	default:
		return nil
	}
}

func findMemberLeafDir(suiteRoot, memberID string) (string, bool) {
	ents, err := os.ReadDir(suiteRoot)
	if err != nil {
		return "", false
	}
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		leaf := filepath.Join(suiteRoot, ent.Name())
		if _, err := os.Stat(filepath.Join(leaf, "extension.yaml")); err != nil {
			continue
		}
		man, err := LoadManifest(leaf)
		if err != nil {
			continue
		}
		if strings.TrimSpace(man.ID) == memberID {
			return leaf, true
		}
	}
	return "", false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
