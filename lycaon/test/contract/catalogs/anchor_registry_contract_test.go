package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestCatalogClosureBindingsAndEnvelopes(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	catalogIDs, envelopes := loadCatalogAnchors(t, root)
	reg := catalogfixture.LoadInformBindings(t)
	indexWorkflowInjects(t, reg)

	var missing []string
	for id, env := range envelopes {
		if !env {
			missing = append(missing, id+": catalog row missing envelope")
		}
	}
	contractcheck.FailViolations(t, "catalog anchors missing envelope", missing)

	missing = missing[:0]
	for _, b := range reg.AllInform() {
		if b == nil {
			continue
		}
		on := string(b.On)
		if !catalogIDs[on] {
			missing = append(missing, on+": Binding.on not in catalog")
		}
		if b.Render == "" {
			missing = append(missing, on+": inform Binding missing render")
			continue
		}
		if _, err := catalogfixture.StockGuidancePath(b.Render); err != nil {
			missing = append(missing, on+" → "+b.Render+": template missing")
		}
	}
	// Workflow injects are indexed separately — also close them.
	for _, b := range reg.BindingsOn(anchor.PhaseEntered) {
		if b == nil || !b.IsInform() {
			continue
		}
		on := string(b.On)
		if !catalogIDs[on] {
			missing = append(missing, on+": phase.entered Binding.on not in catalog")
		}
		if b.Render == "" {
			continue
		}
		if _, err := catalogfixture.StockGuidancePath(b.Render); err != nil {
			missing = append(missing, on+" workflow inject → "+b.Render+": template missing")
		}
	}
	contractcheck.FailViolations(t, "Binding → catalog/template closure", missing)
}

func TestInformWhenConditionWired(t *testing.T) {
	t.Parallel()
	reg := catalogfixture.LoadInformBindings(t)
	indexWorkflowInjects(t, reg)
	var withWhen int
	for _, b := range append(reg.AllInform(), reg.BindingsOn(anchor.PhaseEntered)...) {
		if b != nil && strings.TrimSpace(b.When) != "" {
			withWhen++
		}
	}
	if withWhen < 1 {
		t.Fatal("requires at least one conditioned inform binding")
	}
}

func loadCatalogAnchors(t *testing.T, root string) (ids map[string]bool, hasEnvelope map[string]bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml"))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	var doc struct {
		Anchors []struct {
			ID       string    `yaml:"id"`
			Envelope yaml.Node `yaml:"envelope"`
		} `yaml:"anchors"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("catalog yaml: %v", err)
	}
	ids = map[string]bool{}
	hasEnvelope = map[string]bool{}
	for _, a := range doc.Anchors {
		id := strings.TrimSpace(a.ID)
		if id == "" {
			continue
		}
		ids[id] = true
		hasEnvelope[id] = a.Envelope.Kind != 0
	}
	return ids, hasEnvelope
}

// indexWorkflowInjects indexes every stock workflow inject into reg, read from
// the manifest YAML.
func indexWorkflowInjects(t *testing.T, reg *anchor.Registry) {
	t.Helper()
	for _, wfRoot := range catalogfixture.StockWorkflowRoots(t) {
		_ = wfRoot.Walk(func(at extpacks.Source, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != "workflow.yaml" {
				return nil
			}
			raw, err := at.Read()
			if err != nil {
				return nil
			}
			var doc struct {
				ID      string `yaml:"id"`
				Injects []struct {
					On       string          `yaml:"on"`
					Selector anchor.Selector `yaml:"selector"`
					Effect   string          `yaml:"effect"`
					When     string          `yaml:"when"`
					Render   string          `yaml:"render"`
					Tier     string          `yaml:"tier"`
				} `yaml:"injects"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				return nil
			}
			wf := strings.TrimSpace(doc.ID)
			for _, inj := range doc.Injects {
				b := &anchor.Binding{
					On:       anchor.ID(strings.TrimSpace(inj.On)),
					Selector: inj.Selector,
					Effect:   strings.TrimSpace(inj.Effect),
					When:     strings.TrimSpace(inj.When),
					Render:   strings.TrimSpace(inj.Render),
					Tier:     "workflow",
				}
				if b.Selector.Workflow == nil && wf != "" {
					w := wf
					b.Selector.Workflow = &w
				}
				reg.IndexBinding(b)
			}
			return nil
		})
	}
}
