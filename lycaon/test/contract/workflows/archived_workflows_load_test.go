package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestArchivedWorkflowsLoadWithKnownFields verifies that every archived workflow
// parses cleanly with KnownFields(true) and format defaults applied.
func TestArchivedWorkflowsLoadWithKnownFields(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	packsRoot := filepath.Join(root, "lycaon", "config", "packs")

	var archiveManifestPaths []string
	err := filepath.WalkDir(packsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "workflow.yaml" && filepath.Base(filepath.Dir(filepath.Dir(path))) == "archive" {
			archiveManifestPaths = append(archiveManifestPaths, path)
		}
		return nil
	})
	contractcheck.FailErr(t, "walking packs for archived workflow manifests", err)

	if len(archiveManifestPaths) == 0 {
		t.Fatalf("expected at least one archived workflow manifest under %s", packsRoot)
	}

	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "load full workflow registry", err)

	for _, manifestPath := range archiveManifestPaths {
		t.Run(manifestPath, func(t *testing.T) {
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatalf("reading %s: %v", manifestPath, err)
			}

			// Parse with strict KnownFields(true) and format validation
			m, err := workflowdef.ParseManifestYAML(data)
			if err != nil {
				t.Fatalf("ParseManifestYAML(%s) with KnownFields failed: %v", manifestPath, err)
			}

			if m.ID == "" {
				t.Errorf("%s has empty workflow ID", manifestPath)
			}
			if m.Version == "" {
				t.Errorf("%s has empty workflow Version", manifestPath)
			}
			if m.Format < 1 {
				t.Errorf("%s has invalid format level %d", manifestPath, m.Format)
			}
			if len(m.PhaseDefs) == 0 {
				t.Errorf("%s has no phase definitions", manifestPath)
			}

			// Check that the loader registers it as sealed and retired in the catalog
			registered, err := reg.Get(m.ID, m.Version)
			if err != nil {
				t.Fatalf("registry.Get(%q, %q): %v", m.ID, m.Version, err)
			}
			if !registered.Sealed {
				t.Errorf("%s@%s should be marked Sealed in registry", m.ID, m.Version)
			}
			if !registered.Retired {
				t.Errorf("%s@%s should be marked Retired in registry", m.ID, m.Version)
			}

			// Sealed workflows must not be startable as new runs
			if reg.CatalogStartable(m.ID, m.Version) {
				t.Errorf("%s@%s is sealed and must not be startable", m.ID, m.Version)
			}
		})
	}
}
