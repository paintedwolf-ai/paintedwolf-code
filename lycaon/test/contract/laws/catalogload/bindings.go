package catalogload

import (
	"fmt"
	"io/fs"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	"github.com/lycaon/lycaon/test/contract/laws"
)

const guidanceDir = "lycaon/config/packs/painted-wolf/platform/guidance"

// bindings returns the anchor registry with every stock workflow's injects
// indexed beside the builtin bindings.
func bindings(r *laws.Run) *anchor.Registry {
	return laws.Part(r.Corpus, "anchor bindings", func() (*anchor.Registry, error) {
		reg, err := anchor.LoadRegistryFromConfigRoot()
		if err != nil {
			return nil, err
		}
		packs, err := extpacks.DiscoverStock()
		if err != nil {
			return nil, err
		}
		roots := extpacks.KindDirs(packs, "workflows")
		if len(roots) == 0 {
			return nil, fmt.Errorf("no stock workflow directories")
		}
		manifests := 0
		for _, root := range roots {
			err := root.Walk(func(at extpacks.Source, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || d.Name() != "workflow.yaml" {
					return err
				}
				manifests++
				raw, err := at.Read()
				if err != nil {
					return err
				}
				var doc struct {
					ID      string `yaml:"id"`
					Injects []struct {
						On       string          `yaml:"on"`
						Selector anchor.Selector `yaml:"selector"`
						Effect   string          `yaml:"effect"`
						When     string          `yaml:"when"`
						Render   string          `yaml:"render"`
					} `yaml:"injects"`
				}
				if err := yaml.Unmarshal(raw, &doc); err != nil {
					return fmt.Errorf("%s: %w", at, err)
				}
				workflow := strings.TrimSpace(doc.ID)
				for _, inj := range doc.Injects {
					b := &anchor.Binding{
						On: anchor.ID(strings.TrimSpace(inj.On)), Selector: inj.Selector, Effect: strings.TrimSpace(inj.Effect),
						When: strings.TrimSpace(inj.When), Render: strings.TrimSpace(inj.Render), Tier: "workflow",
					}
					if b.Selector.Workflow == nil && workflow != "" {
						b.Selector.Workflow = &workflow
					}
					reg.IndexBinding(b)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
		if manifests == 0 {
			return nil, fmt.Errorf("no workflow.yaml in the stock workflow directories")
		}
		return reg, nil
	})
}

var bindingClosure = []laws.Rule{{
	// Every binding fires on a catalogued anchor and renders a stock
	// template, and every catalogued anchor declares its envelope.
	Name:    "anchor-bindings-close-over-catalog-and-templates",
	Anchors: []laws.Anchor{laws.File("lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")},
	Check: func(r *laws.Run) {
		var doc struct {
			Anchors []struct {
				ID       string    `yaml:"id"`
				Envelope yaml.Node `yaml:"envelope"`
			} `yaml:"anchors"`
		}
		r.Must(yaml.Unmarshal(r.Bytes("lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml"), &doc))
		catalogued := map[string]bool{}
		for _, a := range doc.Anchors {
			if id := strings.TrimSpace(a.ID); id != "" {
				catalogued[id] = true
				if a.Envelope.Kind == 0 {
					r.Report("anchor %s declares no envelope", id)
				}
			}
		}
		r.Expect(len(catalogued), "catalogued anchors")
		reg := bindings(r)
		check := func(b *anchor.Binding, kind string) {
			if b == nil || !b.IsInform() {
				return
			}
			if !catalogued[string(b.On)] {
				r.Report("%s binding on %s fires on an uncatalogued anchor", kind, b.On)
			}
			if b.Render == "" {
				if kind == "inform" {
					r.Report("inform binding on %s renders nothing", b.On)
				}
				return
			}
			if _, err := catalogfixture.StockGuidancePath(b.Render); err != nil {
				r.Report("%s binding on %s renders the missing template %s", kind, b.On, b.Render)
			}
		}
		for _, b := range reg.AllInform() {
			check(b, "inform")
		}
		for _, b := range reg.BindingsOn(anchor.PhaseEntered) {
			check(b, "phase.entered")
		}
	},
}, {
	// Every guidance template is rendered by some inform binding.
	Name:    "guidance-templates-have-inform-bindings",
	Anchors: []laws.Anchor{laws.Dir(guidanceDir)},
	Check: func(r *laws.Run) {
		reg := bindings(r)
		renders := map[string]bool{}
		for _, b := range append(reg.AllInform(), reg.BindingsOn(anchor.PhaseEntered)...) {
			if b != nil && b.Render != "" {
				renders[b.Render] = true
			}
		}
		for _, rel := range r.Glob(guidanceDir + "/*.md") {
			stem := strings.TrimSuffix(rel[strings.LastIndex(rel, "/")+1:], ".md")
			if !renders[stem] {
				r.Report("%s has no inform binding that renders it", rel)
			}
		}
	},
}}
