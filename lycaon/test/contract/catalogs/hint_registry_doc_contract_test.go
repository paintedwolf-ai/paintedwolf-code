package contract

import (
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// stockPolicyIdentities reads every policy document the stock packs ship and
// returns its identity: namespace/id for a document with spec_version,
// otherwise the file stem.
func stockPolicyIdentities(t *testing.T) []string {
	t.Helper()
	packs, err := extpacks.DiscoverStock()
	contractcheck.FailErr(t, "discover stock packs", err)
	dirs := extpacks.KindDirs(packs, "policy")
	if len(dirs) == 0 {
		t.Fatal("no stock pack ships a policy directory")
	}
	seen := map[string]bool{}
	for _, dir := range dirs {
		err := dir.Walk(func(at extpacks.Source, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			ext := path.Ext(d.Name())
			if ext != ".yaml" && ext != ".yml" && ext != ".json" {
				return nil
			}
			raw, err := at.Read()
			if err != nil {
				return err
			}
			var doc struct {
				SpecVersion any    `yaml:"spec_version"`
				ID          string `yaml:"id"`
				Namespace   string `yaml:"namespace"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				return err
			}
			identity := strings.TrimSuffix(d.Name(), ext)
			if doc.SpecVersion != nil {
				identity = doc.ID
				if doc.Namespace != "" {
					identity = doc.Namespace + "/" + doc.ID
				}
			}
			seen[identity] = true
			return nil
		})
		contractcheck.FailErr(t, "walk "+dir.String(), err)
	}
	out := make([]string, 0, len(seen))
	for identity := range seen {
		out = append(out, identity)
	}
	slices.Sort(out)
	return out
}

// TestBundledHintRegistryIsStockPackUnion requires the effective policy
// registry to hold exactly one entry per policy document the stock packs ship.
func TestBundledHintRegistryIsStockPackUnion(t *testing.T) {
	t.Parallel()
	want := stockPolicyIdentities(t)
	entries, err := hintregistry.ListEffectiveWithCatalog(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "hintregistry.ListEffectiveWithCatalog", err)
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Code)
	}
	slices.Sort(got)
	if len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Fatalf("registry repeats a policy code: %v", got)
	}
	contractcheck.FailSetEqual(t, "effective policy codes vs stock policy documents", want, got)
}
