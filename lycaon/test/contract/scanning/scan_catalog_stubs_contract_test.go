package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestDeferredScanCatalogStubsNotInBundledWorkflows(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "workflows")
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read directory entries", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		contractcheck.FailErr(t, "read file", err)
		var doc struct {
			Phases []struct {
				ID           string `yaml:"id"`
				CompleteWhen string `yaml:"complete_when"`
			} `yaml:"phases"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			contractcheck.FailErr(t, "unmarshal YAML document", err)
		}
		for _, p := range doc.Phases {
			cw := strings.TrimSpace(p.CompleteWhen)
			if cw == "" {
				continue
			}
			for _, stub := range conditions.ScanCatalogStubIDs() {
				if strings.Contains(cw, stub) {
					t.Fatalf("%s phase %q references deferred scan stub %q in complete_when %q", e.Name(), p.ID, stub, cw)
				}
			}
		}
	}
}

func TestImplementedScanDomainIDsRegisteredOnBoot(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	for _, id := range conditions.ShippedScanDomainIDs() {
		if !reg.Has(id) {
			t.Fatalf("shipped scan domain id %q not registered", id)
		}
	}
}
