package oar

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func hostCapabilityDocumentPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(configlayout.FindModuleRoot(), "config", "packs", "painted-wolf", "platform", "host", "anchors", "oar-profile.yaml")
}

// Each core anchor has exactly one disposition: mapped or unsupported.
func TestHostCapabilityDocumentCoversEveryCoreAnchor(t *testing.T) {
	ensureCatalog(t)
	p, err := LoadCapabilityDocument(hostCapabilityDocumentPath(t))
	testutil.FailErr(t, "load host profile", err)

	if p.Version != "1.0" {
		t.Fatalf("oar_capability_version = %q, want 1.0", p.Version)
	}
	for _, core := range CoreAnchors() {
		_, mapped := p.Anchors.Core[core]
		unsupported := false
		for _, u := range p.Anchors.Unsupported {
			if u == core {
				unsupported = true
			}
		}
		if mapped == unsupported {
			t.Errorf("core anchor %q: mapped=%v unsupported=%v — want exactly one", core, mapped, unsupported)
		}
	}
	for core, local := range p.Anchors.Core {
		if err := anchorcatalog.Require(local); err != nil {
			t.Errorf("core anchor %q maps to %q, which is not in catalog.yaml", core, local)
		}
	}
}

// Rule loading relies on the published fact names and types matching the engine.
func TestCapabilityDocumentMatchesCatalogue(t *testing.T) {
	ensureCatalog(t)
	p, err := LoadCapabilityDocument(hostCapabilityDocumentPath(t))
	testutil.FailErr(t, "load host profile", err)

	// Host facts use their published names ([OAR-FACT-18]).
	catalogue := map[string]string{}
	eachDeclaredFact(func(d factDecl) {
		if d.tier != FactTierHost {
			return
		}
		name := publishedName(d.name, d.tier)
		if _, duplicate := catalogue[name]; duplicate {
			t.Errorf("host fact %q has more than one source declaration", name)
		}
		catalogue[name] = factTypeString(d.typ)
	})
	for _, f := range observationFns {
		if f.tier != FactTierHost {
			continue
		}
		catalogue[publishedName(f.name, f.tier)] = "(string) -> " + factTypeString(f.ret)
	}

	declared := map[string]string{}
	for _, hf := range p.HostFacts {
		name := strings.TrimSpace(hf.Name)
		if _, dup := declared[name]; dup {
			t.Errorf("oar-profile.yaml declares host fact %q twice", name)
		}
		declared[name] = strings.TrimSpace(hf.Type)
	}

	for name, typ := range catalogue {
		got, ok := declared[name]
		if !ok {
			t.Errorf("catalogue.go declares host fact %q, which oar-profile.yaml does not publish", name)
			continue
		}
		if got != typ {
			t.Errorf("host fact %q is published as %q, catalogue.go declares %q", name, got, typ)
		}
	}
	for name := range declared {
		if _, ok := catalogue[name]; !ok {
			t.Errorf("oar-profile.yaml publishes host fact %q, which catalogue.go does not declare", name)
		}
	}
}

// TestCapabilityDocumentRejectsMalformedMaps checks clause and field tokens in load failures.
func TestCapabilityDocumentRejectsMalformedMaps(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "both",
			yaml: "oar_capability_version: \"1.0\"\nprofiles: []\nanchors:\n  core:\n    tool.pre_invoke: tool.pre_invoke\n    tool.handler: tool.handler\n    tool.post_invoke: tool.post_invoke\n    agent.post_turn: coordinator.post_turn\n    agent.finalize: worker.finalize\n    model.input: content.input\n  unsupported: [model.input, model.output, model.tool_result]\n",
			want: `[OAR-PROF-2]`,
		},
		{
			name: "missing",
			yaml: "oar_capability_version: \"1.0\"\nprofiles: []\nanchors:\n  core:\n    tool.pre_invoke: tool.pre_invoke\n  unsupported: [model.input, model.output, model.tool_result]\n",
			want: `tool.handler`,
		},
		{
			name: "unknown_local",
			yaml: "oar_capability_version: \"1.0\"\nprofiles: []\nanchors:\n  core:\n    tool.pre_invoke: tool.pre_invoke\n    tool.handler: tool.handler\n    tool.post_invoke: tool.post_invoke\n    agent.post_turn: coordinator.post_turn\n    agent.finalize: nope.nope\n  unsupported: [model.input, model.output, model.tool_result]\n",
			want: `core anchor "agent.finalize" maps to "nope.nope", which is not an installed catalog anchor`,
		},
		{
			name: "not_a_core_anchor",
			yaml: "oar_capability_version: \"1.0\"\nprofiles: []\nanchors:\n  core:\n    tool.sideways: tool.pre_invoke\n  unsupported: []\n",
			want: `tool.sideways`,
		},
	}
	ensureCatalog(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCapabilityDocument([]byte("host: paintedwolf\nactivity_window: 0\ndetectors: []\nexpression_nodes_max: 256\nsupports_transform: false\n" + tc.yaml))
			if err == nil {
				t.Fatal("expected a load error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestResolveRuleAnchorThroughProfile proves core anchors resolve to local ids
// through the host capability document, including the model-IO family.
func TestResolveRuleAnchorThroughProfile(t *testing.T) {
	ensureCatalog(t)
	p, err := LoadCapabilityDocument(hostCapabilityDocumentPath(t))
	testutil.FailErr(t, "load host profile", err)
	prev := InstalledCapabilityDocument()
	InstallCapabilityDocument(p)
	t.Cleanup(func() { InstallCapabilityDocument(prev) })

	got, err := ResolveRuleAnchor("R", CoreAnchorAgentPostTurn)
	testutil.FailErr(t, "resolve agent.post_turn", err)
	if got != "coordinator.post_turn" {
		t.Fatalf("agent.post_turn resolved to %q", got)
	}
	got, err = ResolveRuleAnchor("R", "coordinator.pre_invoke")
	testutil.FailErr(t, "resolve host-native anchor", err)
	if got != "coordinator.pre_invoke" {
		t.Fatalf("host-native anchor rewritten to %q", got)
	}
	got, err = ResolveRuleAnchor("R", CoreAnchorModelInput)
	testutil.FailErr(t, "resolve model.input", err)
	if got != "content.input" {
		t.Fatalf("model.input resolved to %q, want content.input", got)
	}
}

func TestCoreAnchorResolutionRequiresCapabilityDocument(t *testing.T) {
	var profile *CapabilityDocument
	if _, err := profile.ResolveAnchor(CoreAnchorModelInput); err == nil {
		t.Fatal("expected core anchor resolution without a capability document to fail")
	}
	if _, err := profile.ResolveAnchor("coordinator.pre_invoke"); err == nil {
		t.Fatal("[OAR-PROF-5] native anchors require a capability declaration")
	}
}

// Core anchors map through the capability document; host-native anchors keep their names.
func TestShippedRuleAnchorsResolveThroughTheCapabilityDocument(t *testing.T) {
	ensureCatalog(t)
	l, err := NewLoader("")
	testutil.FailErr(t, "new loader", err)

	p, err := LoadCapabilityDocument(hostCapabilityDocumentPath(t))
	testutil.FailErr(t, "load capability document", err)
	prev := InstalledCapabilityDocument()
	InstallCapabilityDocument(p)
	t.Cleanup(func() { InstallCapabilityDocument(prev) })

	rs, err := l.LoadEffectivePolicy()
	testutil.FailErr(t, "load stock", err)
	if rs.Len() == 0 {
		t.Fatal("no shipped rules loaded")
	}

	local := map[string]bool{}
	for _, name := range p.Anchors.Core {
		local[name] = true
	}
	for _, r := range rs.All() {
		// Every resolved anchor is a local catalog id.
		if IsCoreAnchor(r.Anchor) && !local[r.Anchor] {
			t.Errorf("rule %s resolved to core anchor %q, which no local anchor implements", r.ID, r.Anchor)
		}
		if err := RequireKnownCatalogAnchor(r.Anchor); err != nil {
			t.Errorf("rule %s: %v", r.ID, err)
		}
	}

	// Every core anchor is mapped; none remain unsupported on this host.
	if len(p.UnsupportedCoreAnchors()) != 0 {
		t.Fatalf("unsupported core anchors = %v, want none", p.UnsupportedCoreAnchors())
	}
	got, err := ResolveRuleAnchor("PROBE", CoreAnchorModelInput)
	testutil.FailErr(t, "resolve model.input", err)
	if got != "content.input" {
		t.Fatalf("model.input → %q", got)
	}
}
