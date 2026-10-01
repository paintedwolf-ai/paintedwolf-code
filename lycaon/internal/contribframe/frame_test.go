package contribframe

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMCPRequirementPreservesConfiguredProviderIdentity(t *testing.T) {
	for _, providerID := range []string{"tracker", "Orchard native tracker", "Café tracker", "org/tracker.v2"} {
		t.Run(providerID, func(t *testing.T) {
			set, err := contribution.Compile(contribution.CompileInput{Units: []contribution.Input{
				unit(contribution.KindMCPRequirement, "acme/reviewer", "tracker",
					fmt.Sprintf("id: acme/reviewer:tracker\nprovider_id: %q\nrequired_tools: [search_issues]\npurpose: Search issues\n", providerID)),
			}})
			testutil.FailErr(t, "compile configured provider requirement", err)
			view := &catalogview.View{Catalog: &extpacks.EffectiveCatalog{Revision: "catalog"}, Contributions: set}
			for _, candidate := range []string{providerID, providerID + " other"} {
				gen := readyGeneration(candidate)
				gen.Providers[0].ID = candidate
				frame, err := Build(view, gen)
				testutil.FailErr(t, "capture provider identity", err)
				if got := frame.RequirementReady("acme/reviewer:tracker"); got != (candidate == providerID) {
					t.Fatalf("provider %q satisfied requirement %q: %v", candidate, providerID, got)
				}
			}
		})
	}
}

func unit(kind contribution.Kind, provider, name, body string) contribution.Input {
	return contribution.Input{
		UnitID:         "contributions/" + string(kind) + "/" + name,
		Kind:           kind,
		ProviderPackID: provider,
		Body:           []byte(body),
	}
}

func fixtureView(t *testing.T, catalogRevision string) *catalogview.View {
	t.Helper()
	set, err := contribution.Compile(contribution.CompileInput{
		Units: []contribution.Input{
			unit(contribution.KindCommand, "acme/reviewer", "search",
				"id: acme/reviewer:search\ntitle: Search issues\naction:\n  kind: mcp_tool\n  requirement: acme/reviewer:tracker\n  tool: search_issues\n"),
			unit(contribution.KindCommand, "acme/reviewer", "go-home",
				"id: acme/reviewer:go-home\ntitle: Go home\naction: {kind: navigate, destination: home}\n"),
			unit(contribution.KindMCPRequirement, "acme/reviewer", "tracker",
				"id: acme/reviewer:tracker\nprovider_id: tracker\nrequired_tools: [search_issues]\npurpose: Search the tracker\n"),
		},
	})
	testutil.FailErr(t, "compile fixture set", err)
	return &catalogview.View{
		Catalog:       &extpacks.EffectiveCatalog{Revision: catalogRevision, Loaded: map[string]extpacks.UnitEffective{}},
		Contributions: set,
	}
}

func readyGeneration(revision string) *mcp.ResourceGeneration {
	return &mcp.ResourceGeneration{
		Revision: revision,
		Providers: []mcp.GenerationProvider{{
			ID: "tracker", Enabled: true, Status: api.McpStatusReady,
			Tools: []mcp.GenerationTool{{Name: "search_issues", Fingerprint: "fp1"}},
		}},
	}
}

func id(s string) contribution.ID {
	parsed, err := contribution.ParseID(s)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestBuildJoinsRequirementsAgainstCapturedGeneration(t *testing.T) {
	view := fixtureView(t, "cat-rev")

	cases := []struct {
		name   string
		gen    *mcp.ResourceGeneration
		ready  bool
		reason string
	}{
		{"ready", readyGeneration("g1"), true, ""},
		{"provider missing", &mcp.ResourceGeneration{Revision: "g2"}, false, "provider_missing"},
		{"provider disabled", &mcp.ResourceGeneration{Revision: "g3", Providers: []mcp.GenerationProvider{{
			ID: "tracker", Enabled: false, Status: api.McpStatusDisabled,
		}}}, false, "provider_disabled"},
		{"provider not ready", &mcp.ResourceGeneration{Revision: "g4", Providers: []mcp.GenerationProvider{{
			ID: "tracker", Enabled: true, Status: api.McpStatusNeedsAuth,
		}}}, false, "provider_not_ready"},
		{"tool missing", &mcp.ResourceGeneration{Revision: "g5", Providers: []mcp.GenerationProvider{{
			ID: "tracker", Enabled: true, Status: api.McpStatusReady,
			Tools: []mcp.GenerationTool{{Name: "other_tool"}},
		}}}, false, "tool_missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := Build(view, tc.gen)
			testutil.FailErr(t, "Build", err)
			status, ok := frame.Requirement(id("acme/reviewer:tracker"))
			if !ok || status.Ready != tc.ready || status.Reason != tc.reason {
				t.Fatalf("requirement = %+v ok=%v", status, ok)
			}
			if frame.RequirementReady("acme/reviewer:tracker") != tc.ready {
				t.Fatal("RequirementReady must agree with the joined status")
			}
		})
	}
}

func TestFrameRevisionCompositesBothGenerations(t *testing.T) {
	view := fixtureView(t, "cat-rev")
	first, err := Build(view, readyGeneration("g1"))
	testutil.FailErr(t, "Build", err)
	same, err := Build(view, readyGeneration("g1"))
	testutil.FailErr(t, "Build again", err)
	if first.Revision != same.Revision {
		t.Fatal("identical generations must yield an identical frame revision")
	}
	changedMCP, err := Build(view, readyGeneration("g2"))
	testutil.FailErr(t, "Build changed mcp", err)
	if changedMCP.Revision == first.Revision {
		t.Fatal("a changed MCP revision must change the frame revision")
	}
	changedCatalog, err := Build(fixtureView(t, "cat-rev-2"), readyGeneration("g1"))
	testutil.FailErr(t, "Build changed catalog", err)
	if changedCatalog.Revision == first.Revision {
		t.Fatal("a changed catalog revision must change the frame revision")
	}
}

func TestSubgraphIdentityIsolatesUnrelatedChurn(t *testing.T) {
	view := fixtureView(t, "cat-rev")
	ready, err := Build(view, readyGeneration("g1"))
	testutil.FailErr(t, "Build ready", err)
	broken, err := Build(view, &mcp.ResourceGeneration{Revision: "g9"})
	testutil.FailErr(t, "Build broken", err)

	if ready.Revision == broken.Revision {
		t.Fatal("fixture frames must differ")
	}
	// Provider readiness does not affect navigation.
	navReady, _ := ready.SubgraphIdentity(id("acme/reviewer:go-home"))
	navBroken, _ := broken.SubgraphIdentity(id("acme/reviewer:go-home"))
	if navReady == "" || navReady != navBroken {
		t.Fatalf("unrelated churn changed a navigate subgraph: %q vs %q", navReady, navBroken)
	}
	// MCP readiness affects MCP-backed commands.
	mcpReady, _ := ready.SubgraphIdentity(id("acme/reviewer:search"))
	mcpBroken, _ := broken.SubgraphIdentity(id("acme/reviewer:search"))
	if mcpReady == "" || mcpReady == mcpBroken {
		t.Fatal("requirement readiness must participate in the mcp command subgraph")
	}
}

func typedFeatureView(t *testing.T, operationTool string) *catalogview.View {
	t.Helper()
	set, err := contribution.Compile(contribution.CompileInput{Units: []contribution.Input{
		unit(contribution.KindMCPRequirement, "acme/issues", "tracker", "id: acme/issues:tracker\nprovider_id: tracker\nrequired_tools: [create, search]\npurpose: Issues\n"),
		unit(contribution.KindOperation, "acme/issues", "create", "id: acme/issues:create\ninput: [{id: title, type: string, required: true}]\noutput: [{id: issue-id, type: string, required: true}]\naction: {kind: mcp_tool, requirement: acme/issues:tracker, tool: "+operationTool+"}\n"),
		unit(contribution.KindCommand, "acme/issues", "create-command", "id: acme/issues:create-command\ntitle: Create issue\ncategory: Issues\nicon: tool\nenablement: {fact: mcp_requirement_ready, is: acme/issues:tracker}\ninput: [{id: title, title: Title, type: string, required: true}]\naction: {kind: operation, ref: acme/issues:create}\n"),
		unit(contribution.KindSearchSource, "acme/issues", "search", "id: acme/issues:search\nlabel: Issues\nprefix: issues\nrequirement: acme/issues:tracker\ntool: search\nquery: {min_length: 2}\nresult: {id: id, title: title, arguments: activation}\nactivation:\n  command: acme/issues:create-command\n  input: [{id: title, type: string, required: true}]\n"),
	}})
	testutil.FailErr(t, "compile typed feature", err)
	return &catalogview.View{Catalog: &extpacks.EffectiveCatalog{Revision: "catalog", Loaded: map[string]extpacks.UnitEffective{}}, Contributions: set}
}

func TestProjectCarriesCompleteCommandAndProviderSourceContract(t *testing.T) {
	view := typedFeatureView(t, "create")
	frame, err := Build(view, &mcp.ResourceGeneration{Revision: "mcp", Providers: []mcp.GenerationProvider{{
		ID: "tracker", Enabled: true, Status: api.McpStatusReady,
		Tools: []mcp.GenerationTool{{Name: "create"}, {Name: "search", ReadOnly: true}},
	}}})
	testutil.FailErr(t, "Build", err)
	projected := Project(frame)
	if len(projected.Commands) != 1 {
		t.Fatalf("commands = %+v", projected.Commands)
	}
	command := projected.Commands[0]
	if command.Category != "Issues" || command.Icon != "tool" || command.ResultTreatment != "output" || command.Enablement == nil || len(command.Input) != 1 {
		t.Fatalf("command = %+v", command)
	}
	if len(projected.SearchSources) != 1 || !projected.SearchSources[0].Ready || projected.SearchSources[0].ActivationCommand != command.ID {
		t.Fatalf("search sources = %+v", projected.SearchSources)
	}
	if len(projected.Operations) != 1 || len(projected.Operations[0].Output) != 1 || projected.Operations[0].Output[0].ID != "issue-id" {
		t.Fatalf("operations = %+v", projected.Operations)
	}
}

func TestProjectDisablesSearchSourceWhenToolIsNotReadOnly(t *testing.T) {
	view := typedFeatureView(t, "create")
	frame, err := Build(view, &mcp.ResourceGeneration{Revision: "mcp", Providers: []mcp.GenerationProvider{{
		ID: "tracker", Enabled: true, Status: api.McpStatusReady,
		Tools: []mcp.GenerationTool{{Name: "create"}, {Name: "search"}},
	}}})
	testutil.FailErr(t, "Build", err)

	projected := Project(frame)
	if len(projected.SearchSources) != 1 || projected.SearchSources[0].Ready || projected.SearchSources[0].DisabledReason != "tool_not_read_only" {
		t.Fatalf("search sources = %+v", projected.SearchSources)
	}
}

func TestOperationBytesParticipateInCommandSubgraphIdentity(t *testing.T) {
	generation := &mcp.ResourceGeneration{Revision: "mcp"}
	first, err := Build(typedFeatureView(t, "create"), generation)
	testutil.FailErr(t, "first Build", err)
	second, err := Build(typedFeatureView(t, "search"), generation)
	testutil.FailErr(t, "second Build", err)
	firstID, _ := first.SubgraphIdentity(id("acme/issues:create-command"))
	secondID, _ := second.SubgraphIdentity(id("acme/issues:create-command"))
	if firstID == secondID {
		t.Fatal("operation change did not invalidate the command subgraph")
	}
}
