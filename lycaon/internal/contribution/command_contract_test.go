package contribution

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandInputContractCoversClosedTypes(t *testing.T) {
	command := input(KindCommand, "acme/issues", "create", `id: acme/issues:create
title: Create issue
icon: tool
enablement: {fact: mcp_requirement_ready, is: "acme/issues:tracker"}
input:
  - {id: title, title: Title, type: string, required: true}
  - {id: count, title: Count, type: number, min: 1, max: 5, default: 2}
  - {id: urgent, title: Urgent, type: boolean, default: false}
  - {id: priority, title: Priority, type: enum, values: [low, high], default: low}
  - {id: labels, title: Labels, type: string_list}
  - {id: target, title: Target, type: project_path}
action: {kind: mcp_tool, requirement: "acme/issues:tracker", tool: create_issue}
result: output
`)
	requirement := input(KindMCPRequirement, "acme/issues", "tracker", `id: acme/issues:tracker
provider_id: tracker
required_tools: [create_issue]
purpose: Create issues.
`)
	set := compileWith(t, CompileInput{Units: []Input{command, requirement}})
	compiled, _ := set.Command(ID{Provider: "acme/issues", Name: "create"})
	if len(compiled.Input) != 6 || compiled.Input[0].ID != "title" || compiled.Input[5].ID != "target" {
		t.Fatalf("ordered input = %+v", compiled.Input)
	}
	resolved, ok := set.ResolveCommand(compiled)
	if !ok || resolved.Result != ResultOutput || resolved.Icon != "tool" {
		t.Fatalf("resolved = %+v ok=%t", resolved, ok)
	}
}

func TestCommandInputRejectsIllegalConstraintsAndConsumers(t *testing.T) {
	cases := map[string]string{
		"input on effect": `id: acme/a:x
title: X
input: [{id: value, title: Value, type: string}]
action: {kind: navigate, destination: home}
`,
		"enum without values": `id: acme/a:x
title: X
input: [{id: value, title: Value, type: enum}]
action: {kind: mcp_tool, requirement: "acme/a:r", tool: t}
`,
		"bounds on string": `id: acme/a:x
title: X
input: [{id: value, title: Value, type: string, min: 1}]
action: {kind: mcp_tool, requirement: "acme/a:r", tool: t}
`,
		"invalid default": `id: acme/a:x
title: X
input: [{id: value, title: Value, type: enum, values: [a, b], default: c}]
action: {kind: mcp_tool, requirement: "acme/a:r", tool: t}
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			faults := compileFaults(t, CompileInput{Units: []Input{input(KindCommand, "acme/a", "x", body)}})
			if faults[0].Code != FaultSchema {
				t.Fatalf("faults = %+v", faults)
			}
		})
	}
}

func TestResultTreatmentMatrix(t *testing.T) {
	cases := []struct {
		kind ActionKind
		good ResultTreatment
		bad  ResultTreatment
	}{
		{ActionMCPTool, ResultDiscard, ResultEffect},
		{ActionNavigate, ResultEffect, ResultOutput},
		{ActionWorkflowStart, ResultReceipt, ResultDiscard},
		{ActionEditorAction, ResultReceipt, ResultOutput},
	}
	for _, tc := range cases {
		testutil.FailErr(t, string(tc.kind)+"/"+string(tc.good), validateResult(tc.kind, tc.good))
		if err := validateResult(tc.kind, tc.bad); err == nil {
			t.Errorf("%s/%s accepted", tc.kind, tc.bad)
		}
	}
}

func TestInteractionIsBoundedAndPriorAnswerOnly(t *testing.T) {
	valid := &Interaction{Steps: []InteractionStep{
		{ID: "repository", Title: "Repository", Kind: InteractionChoice, Source: &DynamicChoiceSource{Kind: ChoiceSourceMCP, Requirement: "acme/a:r", Tool: "list"}},
		{ID: "title", Title: "Title", Kind: InteractionString, Required: true},
		{ID: "confirm", Title: "Confirm", Kind: InteractionConfirmation, Required: true, If: &InteractionPredicate{Step: "title", Is: "ship"}},
	}}
	testutil.FailErr(t, "validate interaction", validateInteraction(valid))
	fields := InteractionFields(valid)
	if fields[0].Type != PropertyString || fields[2].Type != PropertyBoolean {
		t.Fatalf("dynamic/confirmation fields = %+v", fields)
	}

	forward := &Interaction{Steps: []InteractionStep{{ID: "later", Title: "Later", Kind: InteractionString, If: &InteractionPredicate{Step: "future", Is: "x"}}, {ID: "future", Title: "Future", Kind: InteractionString}}}
	if err := validateInteraction(forward); err == nil {
		t.Fatal("forward dependency accepted")
	}
	tooMany := &Interaction{}
	for index := 0; index <= MaxInteractionSteps; index++ {
		tooMany.Steps = append(tooMany.Steps, InteractionStep{ID: "s" + strings.Repeat("x", index), Title: "Step", Kind: InteractionString})
	}
	if err := validateInteraction(tooMany); err == nil {
		t.Fatal("interaction step bound accepted")
	}
}

func TestSearchSourceActivationSchemaIsExact(t *testing.T) {
	requirement := input(KindMCPRequirement, "acme/issues", "tracker", `id: acme/issues:tracker
provider_id: tracker
required_tools: [search, open]
purpose: Search issues.
`)
	command := input(KindCommand, "acme/issues", "open", `id: acme/issues:open
title: Open issue
input: [{id: issue-id, title: Issue, type: string, required: true}]
action: {kind: mcp_tool, requirement: "acme/issues:tracker", tool: open}
result: discard
`)
	source := input(KindSearchSource, "acme/issues", "search", `id: acme/issues:search
label: Issues
prefix: issues
requirement: acme/issues:tracker
tool: search
query: {min_length: 2, max_results: 20}
result: {id: issue_id, title: title, arguments: activation}
activation:
  command: acme/issues:open
  input: [{id: issue-id, type: string, required: true}]
`)
	set := compileWith(t, CompileInput{Units: []Input{requirement, command, source}})
	if len(set.SearchSources()) != 1 || set.SearchSources()[0].Query.MaxResults != 20 {
		t.Fatalf("sources = %+v", set.SearchSources())
	}

	bad := source
	bad.Body = []byte(strings.ReplaceAll(string(source.Body), "type: string, required: true", "type: number, required: true"))
	faults := compileFaults(t, CompileInput{Units: []Input{requirement, command, bad}})
	if !faultContains(faults, "activation.input must exactly match") {
		t.Fatalf("faults = %+v", faults)
	}
}

func TestTypedOperationsRequireDependenciesAndExactSchemas(t *testing.T) {
	requirement := input(KindMCPRequirement, "acme/issues", "tracker", `id: acme/issues:tracker
provider_id: tracker
required_tools: [create]
purpose: Create issues.
`)
	provider := input(KindOperation, "acme/issues", "create", `id: acme/issues:create
input: [{id: title, type: string, required: true}]
output: [{id: issue-id, type: string, required: true}]
action: {kind: mcp_tool, requirement: "acme/issues:tracker", tool: create}
`)
	consumer := input(KindCommand, "acme/release", "track", `id: acme/release:track
title: Create tracking issue
input: [{id: title, title: Title, type: string, required: true}]
action: {kind: operation, ref: "acme/issues:create"}
`)
	depends := func(pack, dependency string) bool { return pack == "acme/release" && dependency == "acme/issues" }
	set := compileWith(t, CompileInput{Units: []Input{requirement, provider, consumer}, PackDependsOn: depends})
	command, _ := set.Command(ID{Provider: "acme/release", Name: "track"})
	resolved, ok := set.ResolveCommand(command)
	if !ok || resolved.Action.Kind != ActionMCPTool || len(resolved.Output) != 1 || len(resolved.Chain) != 1 {
		t.Fatalf("resolved = %+v ok=%t", resolved, ok)
	}

	faults := compileFaults(t, CompileInput{Units: []Input{requirement, provider, consumer}})
	if !faultContains(faults, "explicit manifest dependency") {
		t.Fatalf("faults = %+v", faults)
	}
}

func TestDirectContributionReferencesStayWithinPack(t *testing.T) {
	requirement := input(KindMCPRequirement, "acme/provider", "tracker", `id: acme/provider:tracker
provider_id: tracker
required_tools: [create]
purpose: Create issues.
`)
	command := input(KindCommand, "acme/consumer", "create", `id: acme/consumer:create
title: Create issue
action: {kind: mcp_tool, requirement: "acme/provider:tracker", tool: create}
`)
	faults := compileFaults(t, CompileInput{Units: []Input{requirement, command}})
	if !faultContains(faults, "must belong to the declaring pack") {
		t.Fatalf("faults = %+v", faults)
	}

	condition := input(KindCommand, "acme/consumer", "status", `id: acme/consumer:status
title: Provider status
when: {fact: mcp_requirement_ready, is: "acme/provider:tracker"}
action: {kind: navigate, destination: home}
`)
	faults = compileFaults(t, CompileInput{Units: []Input{requirement, condition}})
	if !faultContains(faults, "must belong to the declaring pack") {
		t.Fatalf("faults = %+v", faults)
	}

	workflow := input(KindCommand, "acme/consumer", "run", `id: acme/consumer:run
title: Run workflow
action: {kind: workflow_start, workflow: provider-flow}
`)
	faults = compileFaults(t, CompileInput{
		Units: []Input{workflow},
		UnitProvider: func(string) (string, bool) {
			return "acme/provider", true
		},
	})
	if !faultContains(faults, "must belong to the declaring pack") {
		t.Fatalf("faults = %+v", faults)
	}
}

func TestOperationGraphRejectsCyclesAndOutputMismatch(t *testing.T) {
	one := input(KindOperation, "acme/a", "one", `id: acme/a:one
output: [{id: value, type: string}]
action: {kind: operation, ref: "acme/a:two"}
`)
	two := input(KindOperation, "acme/a", "two", `id: acme/a:two
output: [{id: value, type: string}]
action: {kind: operation, ref: "acme/a:one"}
`)
	if faults := compileFaults(t, CompileInput{Units: []Input{one, two}}); !faultContains(faults, "cycle") {
		t.Fatalf("cycle faults = %+v", faults)
	}

	two.Body = []byte(strings.ReplaceAll(string(two.Body), "type: string", "type: number"))
	if faults := compileFaults(t, CompileInput{Units: []Input{one, two}}); !faultContains(faults, "operation output must exactly match") {
		t.Fatalf("output faults = %+v", faults)
	}
}

func faultContains(faults []Fault, needle string) bool {
	for _, fault := range faults {
		if strings.Contains(fault.Message, needle) {
			return true
		}
	}
	return false
}
