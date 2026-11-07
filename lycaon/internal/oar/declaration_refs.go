package oar

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/configlayout"
)

// The declaration-reference audit asserts the structural invariant over the
// host fact vocabulary: every declared fact has a reference outside the
// declaration and projection plumbing — a rule condition, a copy binding, a
// guidance entry, provider or test code, or a documented derivation. It
// compares declarations against references and never reads wording.

// declarationPlumbing holds repository-relative paths whose fact names are
// the declaration, registration, projection, or publication side of the
// pipeline. A name appearing there is not consumption.
var declarationPlumbing = map[string]bool{
	"lycaon/config/packs/painted-wolf/platform/host/anchors/oar-profile.yaml": true,
	"lycaon/internal/oar/catalogue.go":                                        true,
	"lycaon/internal/oar/occurrence_facts.go":                                 true,
	"lycaon/internal/oar/fact_projection.go":                                  true,
	"lycaon/internal/oar/facts.go":                                            true,
	"lycaon/internal/oar/conformance.go":                                      true,
	"lycaon/internal/oar/context_facts.go":                                    true,
	"lycaon/internal/oar/context.go":                                          true,
	"lycaon/internal/oar/env.go":                                              true,
}

var (
	qualifiedFactRef = regexp.MustCompile(`paintedwolf\.([A-Za-z0-9_]+)`)
	bareFactLiteral  = regexp.MustCompile(`"([A-Za-z0-9_]+)"`)

	auditSkipDirs = map[string]bool{
		".git": true, ".tmp": true, ".copilot": true, "dist": true,
		"node_modules": true, "target": true, "vendor": true,
	}
	auditTextExt = map[string]bool{
		".go": true, ".json": true, ".md": true, ".py": true, ".sh": true,
		".toml": true, ".ts": true, ".tsx": true, ".yaml": true, ".yml": true,
	}
)

// UnreferencedDeclaredFacts returns declared host facts that have no
// reference outside the declaration and projection plumbing. moduleRoot is
// the lycaon module root; the scan covers the repository around it, including
// the test tree, the den frontend, schemas, and docs. Dynamically constructed
// fact names are matched through their bare string literals, so a fact is
// flagged only when no literal of any form references it. Declared can_*
// facts are referenced by the recovery publication contract itself and are
// checked against the live publication set instead of the text scan.
func UnreferencedDeclaredFacts(moduleRoot string) ([]string, error) {
	profilePath := filepath.Join(moduleRoot, "config", "packs", "painted-wolf",
		"platform", "host", "anchors", "oar-profile.yaml")
	profile, err := LoadCapabilityDocument(profilePath)
	if err != nil {
		return nil, err
	}
	declared := map[string]bool{}
	bare := map[string]string{}
	for _, hf := range profile.HostFacts {
		name := strings.TrimSpace(hf.Name)
		declared[name] = true
		bare[strings.TrimPrefix(name, "paintedwolf.")] = name
	}

	referenced := map[string]bool{}
	repoRoot := filepath.Dir(moduleRoot)
	auditRoot, err := os.OpenRoot(repoRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = auditRoot.Close() }()
	err = filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if auditSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !auditTextExt[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		if declarationPlumbing[rel] || isPublicationPlumbing(rel) {
			return nil
		}
		f, err := auditRoot.Open(rel)
		if err != nil {
			return err
		}
		text, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return err
		}
		for _, m := range qualifiedFactRef.FindAllStringSubmatch(string(text), -1) {
			if declared["paintedwolf."+m[1]] {
				referenced["paintedwolf."+m[1]] = true
			}
		}
		for _, m := range bareFactLiteral.FindAllStringSubmatch(string(text), -1) {
			if name, ok := bare[m[1]]; ok {
				referenced[name] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// RegisterRecoveryFacts derives its lazy provider set from CanCallFacts,
	// so every declared can_* fact is published to the observation stream by
	// the recovery contract regardless of rule or copy references;
	// TestSessionOccurrencesPreserveOfferedRecoverySnapshot pins that
	// publication. Taking the live publication set as the reference keeps the
	// audit structural: a can_* declaration is flagged exactly when the
	// contract stops publishing it.
	for _, name := range CanCallFacts() {
		if declared["paintedwolf."+name] {
			referenced["paintedwolf."+name] = true
		}
	}

	var unreferenced []string
	for name := range declared {
		if !referenced[name] {
			unreferenced = append(unreferenced, name)
		}
	}
	sort.Strings(unreferenced)
	return unreferenced, nil
}

// isPublicationPlumbing reports whether a repository-relative path belongs to
// the publication side of the pipeline, which rewrites raw occurrence keys
// onto declared fact names. Its fact names are outputs, not consumption.
func isPublicationPlumbing(rel string) bool {
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "lycaon/internal/oarcopy/") {
		return false
	}
	return strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go")
}

// AuditDeclarationReferences runs the invariant from the current process and
// returns the unreferenced declared facts, locating the module root from the
// working directory.
func AuditDeclarationReferences() ([]string, error) {
	root := configlayout.FindModuleRoot()
	if root == "" {
		return nil, fmt.Errorf("module root not found from working directory")
	}
	return UnreferencedDeclaredFacts(root)
}
