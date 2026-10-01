package contribution

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func input(kind Kind, pack, stem, body string) Input {
	return Input{
		UnitID:         kind.UnitRoot() + "/" + stem,
		Kind:           kind,
		ProviderPackID: pack,
		Body:           []byte(body),
		Origin:         "test:" + stem,
	}
}

// validReviewerUnits covers the complete intra-pack graph.
func validReviewerUnits() []Input {
	return []Input{
		input(KindEditorAction, "acme/reviewer", "explain",
			"id: acme/reviewer:explain\ntitle: Explain\ntarget:\n  kind: selection\n  required: true\nexecution:\n  preset: inspect_file\n  prompt_ref: guidance/reviewer-explain\n"),
		input(KindCommand, "acme/reviewer", "explain-selection",
			"id: acme/reviewer:explain-selection\ntitle: Explain with Reviewer\ncategory: Reviewer\nscope: files\nkeywords: [review, explain]\nwhen:\n  all:\n    - fact: editor_active\n    - fact: editor_has_selection\naction:\n  kind: editor_action\n  ref: acme/reviewer:explain\n"),
		input(KindMenu, "acme/reviewer", "editor-context",
			"id: acme/reviewer:editor-context\nslot: editor.context.analysis\ncommand: acme/reviewer:explain-selection\ngroup: review\norder: 40\nwhen:\n  all:\n    - fact: editor_has_selection\n"),
		input(KindKeybinding, "acme/reviewer", "explain-binding",
			"id: acme/reviewer:explain-binding\ncommand: acme/reviewer:explain-selection\nscope: files\nbindings:\n  macos: [Mod+Alt+E, \"Leader E\"]\n  windows: [Mod+Shift+E]\n  linux: [Mod+Shift+E]\nwhen:\n  all:\n    - fact: editor_active\n"),
		input(KindConfiguration, "acme/reviewer", "review-depth",
			"id: acme/reviewer:review-depth\ntype: enum\ndescription: How deep reviews look.\ndefault: normal\nenum: [quick, normal, thorough]\nscope: [device, project]\n"),
		input(KindMCPRequirement, "acme/reviewer", "github",
			"id: acme/reviewer:github\nprovider_id: github\nrequired_tools:\n  - get_pull_request\n  - create_review_comment\npurpose: Review and comment on pull requests\n"),
	}
}

func compileWith(t *testing.T, in CompileInput) *Set {
	t.Helper()
	if in.UnitProvider == nil {
		provider := ""
		if len(in.Units) > 0 {
			provider = in.Units[0].ProviderPackID
		}
		in.UnitProvider = func(unitID string) (string, bool) {
			return provider, unitID == "guidance/reviewer-explain"
		}
	}
	set, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return set
}

func compileFaults(t *testing.T, in CompileInput) []Fault {
	t.Helper()
	if in.UnitProvider == nil {
		provider := ""
		if len(in.Units) > 0 {
			provider = in.Units[0].ProviderPackID
		}
		in.UnitProvider = func(unitID string) (string, bool) {
			return provider, unitID == "guidance/reviewer-explain"
		}
	}
	_, err := Compile(in)
	var compileErr *CompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("Compile err = %v, want CompileError", err)
	}
	return compileErr.Faults
}

func singleFault(t *testing.T, in CompileInput, wantCode string) Fault {
	t.Helper()
	faults := compileFaults(t, in)
	if len(faults) != 1 || faults[0].Code != wantCode {
		t.Fatalf("faults = %+v, want one %s", faults, wantCode)
	}
	return faults[0]
}

func TestCompileFullReferentialGraph(t *testing.T) {
	set := compileWith(t, CompileInput{Units: validReviewerUnits()})
	if set.Len() != 6 {
		t.Fatalf("Len = %d want 6", set.Len())
	}
	command, ok := set.Command(ID{Provider: "acme/reviewer", Name: "explain-selection"})
	if !ok || command.Action.Kind != ActionEditorAction {
		t.Fatalf("command = %+v ok=%v", command, ok)
	}
	menus := set.Menus()
	if len(menus) != 1 {
		t.Fatalf("menus = %+v", menus)
	}
	if menus[0].Slot != MenuSlot("editor.context.analysis") {
		t.Fatalf("menus = %+v", menus)
	}
	if len(set.Keybindings()) != 1 || len(set.Settings()) != 1 || len(set.MCPRequirements()) != 1 {
		t.Fatal("typed accessors must expose every compiled declaration")
	}
	defaults := set.BindingDefaults()
	if len(defaults) != 4 {
		t.Fatalf("binding defaults = %+v", defaults)
	}
	for _, def := range defaults {
		if def.Active == nil {
			t.Fatalf("uncontested default must be active: %+v", def)
		}
	}
}

func TestCompileEmptyInputsIsValid(t *testing.T) {
	set := compileWith(t, CompileInput{})
	if set.Len() != 0 {
		t.Fatalf("empty compile Len = %d", set.Len())
	}
}

func TestUnknownFieldsRejectEachKind(t *testing.T) {
	cases := []Input{
		input(KindCommand, "acme/a", "x", "id: acme/a:x\ntitle: T\naction: {kind: navigate, destination: home}\nmystery: 1\n"),
		input(KindMenu, "acme/a", "x", "id: acme/a:x\nslot: editor.context.analysis\ncommand: acme/a:c\nmystery: 1\n"),
		input(KindKeybinding, "acme/a", "x", "id: acme/a:x\ncommand: acme/a:c\nscope: global\nbindings: {macos: [Mod+K]}\nmystery: 1\n"),
		input(KindEditorAction, "acme/a", "x", "id: acme/a:x\ntitle: T\ntarget: {kind: file}\nexecution: {preset: inspect_file, prompt_ref: guidance/reviewer-explain}\nmystery: 1\n"),
		input(KindConfiguration, "acme/a", "x", "id: acme/a:x\ntype: boolean\ndescription: D.\ndefault: true\nmystery: 1\n"),
		input(KindMCPRequirement, "acme/a", "x", "id: acme/a:x\nprovider_id: s\nrequired_tools: [t]\npurpose: P.\nmystery: 1\n"),
		input(KindSearchSource, "acme/a", "x", "id: acme/a:x\nlabel: X\nprefix: x\nrequirement: acme/a:r\ntool: t\nquery: {}\nresult: {id: id, title: title}\nactivation: {command: acme/a:c}\nmystery: 1\n"),
		input(KindOperation, "acme/a", "x", "id: acme/a:x\naction: {kind: mcp_tool, requirement: acme/a:r, tool: t}\nmystery: 1\n"),
	}
	for _, unit := range cases {
		t.Run(string(unit.Kind), func(t *testing.T) {
			fault := singleFault(t, CompileInput{Units: []Input{unit}}, FaultSchema)
			if !strings.Contains(fault.Message, "mystery") {
				t.Fatalf("fault = %+v", fault)
			}
		})
	}
}

func TestMenuStateLabelNeedsState(t *testing.T) {
	unit := input(KindMenu, "acme/a", "x",
		"id: acme/a:x\nslot: app_menu.view\ncommand: acme/a:c\nstate_label: Hide\n")
	fault := singleFault(t, CompileInput{Units: []Input{unit}}, FaultSchema)
	if !strings.Contains(fault.Message, "state_label requires state") {
		t.Fatalf("fault = %+v", fault)
	}
}

func TestClosedVocabulariesReject(t *testing.T) {
	cases := []struct {
		name string
		unit Input
	}{
		{"action kind", input(KindCommand, "acme/a", "x",
			"id: acme/a:x\ntitle: T\naction: {kind: shell_exec, handler: h}\n")},
		{"menu slot", input(KindMenu, "acme/a", "x",
			"id: acme/a:x\nslot: statusbar.left\ncommand: acme/a:c\n")},
		{"keybinding scope", input(KindKeybinding, "acme/a", "x",
			"id: acme/a:x\ncommand: acme/a:c\nscope: everywhere\nbindings: {macos: [Mod+K]}\n")},
		{"binding platform", input(KindKeybinding, "acme/a", "x",
			"id: acme/a:x\ncommand: acme/a:c\nscope: global\nbindings: {beos: [Mod+K]}\n")},
		{"target kind", input(KindEditorAction, "acme/a", "x",
			"id: acme/a:x\ntitle: T\ntarget: {kind: buffer}\nexecution: {preset: inspect_file, prompt_ref: guidance/g}\n")},
		{"preset", input(KindEditorAction, "acme/a", "x",
			"id: acme/a:x\ntitle: T\ntarget: {kind: file}\nexecution: {preset: run_anything, prompt_ref: guidance/g}\n")},
		{"property type", input(KindConfiguration, "acme/a", "x",
			"id: acme/a:x\ntype: secret\ndescription: D.\ndefault: s\n")},
		{"command scope", input(KindCommand, "acme/a", "x",
			"id: acme/a:x\ntitle: T\nscope: universe\naction: {kind: navigate, destination: home}\n")},
		{"navigate destination", input(KindCommand, "acme/a", "x",
			"id: acme/a:x\ntitle: T\naction: {kind: navigate, destination: dashboard}\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := CompileInput{Units: []Input{tc.unit}}
			if strings.HasPrefix(tc.unit.ProviderPackID, "painted-wolf/") {
				in.ProviderRank = func(string) ProviderRank { return RankStock }
			}
			singleFault(t, in, FaultSchema)
		})
	}
}

func TestConditionTyping(t *testing.T) {
	base := "id: acme/a:x\ntitle: T\naction: {kind: navigate, destination: home}\n"
	cases := []struct {
		name string
		when string
		ok   bool
	}{
		{"boolean fact", "when:\n  fact: editor_active\n", true},
		{"operand fact", "when:\n  fact: editor_language\n  is: go\n", true},
		{"nested tree", "when:\n  any:\n    - fact: project_open\n    - not:\n        fact: peer_workspace\n", true},
		{"unknown fact", "when:\n  fact: user_mood\n", false},
		{"operand on boolean", "when:\n  fact: editor_active\n  is: very\n", false},
		{"missing operand", "when:\n  fact: editor_language\n", false},
		{"unknown language", "when:\n  fact: editor_language\n  is: cobol\n", false},
		{"unknown shell view", "when:\n  fact: active_view\n  is: dashboard\n", false},
		{"mixed node", "when:\n  fact: editor_active\n  all:\n    - fact: project_open\n", false},
		{"empty node", "when: {}\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unit := input(KindCommand, "acme/a", "x", base+tc.when)
			in := CompileInput{Units: []Input{unit}}
			if tc.ok {
				compileWith(t, in)
				return
			}
			singleFault(t, in, FaultSchema)
		})
	}
}

func TestConditionRequirementOperandResolves(t *testing.T) {
	command := input(KindCommand, "acme/a", "x",
		"id: acme/a:x\ntitle: T\nwhen:\n  fact: mcp_requirement_ready\n  is: acme/a:github\naction: {kind: navigate, destination: home}\n")
	requirement := input(KindMCPRequirement, "acme/a", "github",
		"id: acme/a:github\nprovider_id: github\nrequired_tools: [get_pull_request]\npurpose: P.\n")
	compileWith(t, CompileInput{Units: []Input{command, requirement}})

	singleFault(t, CompileInput{Units: []Input{command}}, FaultSchema)
}

func TestReferentialIntegrity(t *testing.T) {
	cases := []struct {
		name  string
		units []Input
	}{
		{"command to missing editor action", []Input{
			input(KindCommand, "acme/a", "x",
				"id: acme/a:x\ntitle: T\naction: {kind: editor_action, ref: acme/a:gone}\n"),
		}},
		{"menu to missing command", []Input{
			input(KindMenu, "acme/a", "x",
				"id: acme/a:x\nslot: editor.context.analysis\ncommand: acme/a:gone\n"),
		}},
		{"keybinding to missing command", []Input{
			input(KindKeybinding, "acme/a", "x",
				"id: acme/a:x\ncommand: acme/a:gone\nscope: global\nbindings: {macos: [Mod+K]}\n"),
		}},
		{"mcp_tool to missing requirement", []Input{
			input(KindCommand, "acme/a", "x",
				"id: acme/a:x\ntitle: T\naction: {kind: mcp_tool, requirement: acme/a:gone, tool: t}\n"),
		}},
		{"missing workflow unit", []Input{
			input(KindCommand, "acme/a", "x",
				"id: acme/a:x\ntitle: T\naction: {kind: workflow_start, workflow: gone}\n"),
		}},
		{"missing prompt unit", []Input{
			input(KindEditorAction, "acme/a", "x",
				"id: acme/a:x\ntitle: T\ntarget: {kind: file}\nexecution: {preset: inspect_file, prompt_ref: guidance/gone}\n"),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			singleFault(t, CompileInput{Units: tc.units}, FaultReference)
		})
	}
}

func TestUnreachableCommandFaults(t *testing.T) {
	command := input(KindCommand, "acme/a", "x",
		"id: acme/a:x\ntitle: T\npalette: false\naction: {kind: navigate, destination: home}\n")
	fault := singleFault(t, CompileInput{Units: []Input{command}}, FaultReference)
	if !strings.Contains(fault.Message, "unreachable") || !strings.Contains(fault.Message, "acme/a:x") {
		t.Fatalf("fault = %+v", fault)
	}

	placed := input(KindMenu, "acme/a", "menu-x",
		"id: acme/a:menu-x\nslot: editor.context.analysis\ncommand: acme/a:x\n")
	compileWith(t, CompileInput{Units: []Input{command, placed}})

	bound := input(KindKeybinding, "acme/a", "key-x",
		"id: acme/a:key-x\ncommand: acme/a:x\nscope: global\nbindings: {macos: [Mod+K]}\n")
	compileWith(t, CompileInput{Units: []Input{command, bound}})

	offered := input(KindCommand, "acme/a", "offered",
		"id: acme/a:offered\ntitle: T\nkeywords: [find]\naction: {kind: navigate, destination: home}\n")
	compileWith(t, CompileInput{Units: []Input{offered}})

	hosted := input(KindCommand, "painted-wolf/platform", "run-x",
		"id: painted-wolf/platform:run-x\ntitle: T\npalette: false\nhost_invoked: true\naction: {kind: navigate, destination: home}\n")
	compileWith(t, CompileInput{
		Units:        []Input{hosted},
		ProviderRank: func(string) ProviderRank { return RankStock },
	})
}

func TestMCPToolMustBeDeclared(t *testing.T) {
	command := input(KindCommand, "acme/a", "x",
		"id: acme/a:x\ntitle: T\naction: {kind: mcp_tool, requirement: acme/a:github, tool: delete_repository}\n")
	requirement := input(KindMCPRequirement, "acme/a", "github",
		"id: acme/a:github\nprovider_id: github\nrequired_tools: [get_pull_request]\npurpose: P.\n")
	fault := singleFault(t, CompileInput{Units: []Input{command, requirement}}, FaultReference)
	if !strings.Contains(fault.Message, "delete_repository") {
		t.Fatalf("fault = %+v", fault)
	}
}

func TestNativeActionsAndHostInvocationAreStockOnly(t *testing.T) {
	nonStock := input(KindCommand, "acme/a", "x",
		"id: acme/a:x\ntitle: T\naction: {kind: native_ui, handler: go_context}\n")
	singleFault(t, CompileInput{Units: []Input{nonStock}}, FaultAuthority)

	hostInvoked := input(KindCommand, "acme/a", "hosted",
		"id: acme/a:hosted\ntitle: Hosted\nhost_invoked: true\naction: {kind: navigate, destination: home}\n")
	singleFault(t, CompileInput{Units: []Input{hostInvoked}}, FaultAuthority)
}

func TestKeybindingGrammar(t *testing.T) {
	valid := []string{"Mod+K", "Ctrl+Shift+P", "Super+K", "Escape", "F5", "Leader E", "Mod+Alt+ArrowLeft"}
	for _, binding := range valid {
		if err := validateBinding(binding); err != nil {
			t.Fatalf("%q should parse: %v", binding, err)
		}
	}
	invalid := []string{"", "Meta+K", "Cmd+K", "Mod+Mod+K", "Leader", "Leader Mod+E", "Leader E F", "Mod+", "Banana"}
	for _, binding := range invalid {
		if err := validateBinding(binding); err == nil {
			t.Fatalf("%q should reject", binding)
		}
	}
}

func TestReservedChordsRejectNonStockDefaults(t *testing.T) {
	unit := input(KindKeybinding, "acme/a", "x",
		"id: acme/a:x\ncommand: acme/a:c\nscope: global\nbindings: {macos: [Mod+Q]}\n")
	fault := singleFault(t, CompileInput{Units: []Input{unit}}, FaultAuthority)
	if !strings.Contains(fault.Message, "reserved") {
		t.Fatalf("fault = %+v", fault)
	}
}

func TestPlatformModifierReservationsRejectNonStockDefaults(t *testing.T) {
	for _, test := range []struct {
		platform string
		binding  string
	}{
		{platform: "windows", binding: "Mod+Alt+E"},
		{platform: "linux", binding: "Ctrl+Alt+E"},
		{platform: "windows", binding: "Super+E"},
		{platform: "linux", binding: "Super+E"},
	} {
		t.Run(test.platform+"/"+test.binding, func(t *testing.T) {
			unit := input(KindKeybinding, "acme/a", "x",
				"id: acme/a:x\ncommand: acme/a:c\nscope: global\nbindings: {"+test.platform+": [\""+test.binding+"\"]}\n")
			fault := singleFault(t, CompileInput{Units: []Input{unit}}, FaultAuthority)
			if !strings.Contains(fault.Message, "reserved") {
				t.Fatalf("fault = %+v", fault)
			}
		})
	}
}

func TestAssistiveTechnologyKeysRejectEveryDefault(t *testing.T) {
	for _, stock := range []bool{false, true} {
		binding := &Keybinding{
			Command: "acme/a:c",
			Scope:   "global",
			Bindings: map[string][]string{
				"macos": {"Insert"},
			},
		}
		err := validateKeybinding(binding, stock)
		if err == nil || !strings.Contains(err.Error(), "assistive") {
			t.Fatalf("stock=%t err=%v, want assistive reservation", stock, err)
		}
	}
}

func TestOSInterceptedChordsRejectEveryDefault(t *testing.T) {
	for _, test := range []struct {
		platform string
		binding  string
	}{
		{platform: "macos", binding: "Mod+Space"},
		{platform: "macos", binding: "Shift+Mod+3"},
		{platform: "windows", binding: "Alt+Tab"},
		{platform: "linux", binding: "Super+K"},
	} {
		for _, stock := range []bool{false, true} {
			binding := &Keybinding{
				Command:  "acme/a:c",
				Scope:    "global",
				Bindings: map[string][]string{test.platform: {test.binding}},
			}
			err := validateKeybinding(binding, stock)
			if err == nil || !strings.Contains(err.Error(), "consumes it before the app sees it") {
				t.Fatalf("%s %s stock=%t err=%v, want OS reservation", test.platform, test.binding, stock, err)
			}
		}
	}
	allowed := &Keybinding{
		Command:  "acme/a:c",
		Scope:    "global",
		Bindings: map[string][]string{"macos": {"Mod+Shift+6"}, "windows": {"Mod+Tab"}},
	}
	if err := validateKeybinding(allowed, true); err != nil {
		t.Fatalf("stock chords outside the OS tables: %v", err)
	}
}

func TestUnproduciblePlatformModifiersRejectEveryDefault(t *testing.T) {
	for _, test := range []struct {
		platform string
		binding  string
	}{
		{platform: "macos", binding: "Super+K"},
		{platform: "windows", binding: "Ctrl+K"},
		{platform: "linux", binding: "Mod+Ctrl+K"},
	} {
		binding := &Keybinding{
			Command: "acme/a:c",
			Scope:   "global",
			Bindings: map[string][]string{
				test.platform: {test.binding},
			},
		}
		err := validateKeybinding(binding, true)
		if err == nil || !strings.Contains(err.Error(), "cannot be produced") {
			t.Fatalf("%s %s err=%v, want producibility error", test.platform, test.binding, err)
		}
	}
}

func bindingFixture(pack, stem, chord string) []Input {
	return []Input{
		input(KindCommand, pack, stem+"-cmd",
			"id: "+pack+":"+stem+"-cmd\ntitle: T\naction: {kind: navigate, destination: home}\n"),
		input(KindKeybinding, pack, stem,
			"id: "+pack+":"+stem+"\ncommand: "+pack+":"+stem+"-cmd\nscope: files\nbindings: {macos: [\""+chord+"\"]}\n"),
	}
}

func TestBindingDefaultProvenanceResolution(t *testing.T) {
	units := append(bindingFixture("acme/stock-pack", "one", "Mod+K"),
		bindingFixture("acme/device-pack", "two", "Mod+K")...)
	rank := func(packID string) ProviderRank {
		if packID == "acme/stock-pack" {
			return RankStock
		}
		return RankDevice
	}
	set := compileWith(t, CompileInput{Units: units, ProviderRank: rank})
	var contested *BindingDefault
	for i, def := range set.BindingDefaults() {
		if def.Chord == "Mod+K" {
			contested = &set.BindingDefaults()[i]
		}
	}
	if contested == nil || contested.Active == nil {
		t.Fatalf("contested default = %+v", contested)
	}
	if contested.Active.Provider != "acme/stock-pack" {
		t.Fatalf("higher provenance must stay live: %+v", contested.Active)
	}
	if len(contested.Candidates) != 2 {
		t.Fatalf("conflicts must identify every provider: %+v", contested.Candidates)
	}
	if len(set.Notes()) == 0 || set.Notes()[0].Code != NoteBindingConflict {
		t.Fatalf("notes = %+v", set.Notes())
	}
}

func TestBindingEqualProvenanceDeactivates(t *testing.T) {
	units := append(bindingFixture("acme/one", "one", "Mod+K"),
		bindingFixture("acme/two", "two", "Mod+K")...)
	set := compileWith(t, CompileInput{Units: units})
	for _, def := range set.BindingDefaults() {
		if def.Chord == "Mod+K" && def.Active != nil {
			t.Fatalf("equal-provenance collision must deactivate all: %+v", def)
		}
	}
}

func TestBindingStockCollisionFaults(t *testing.T) {
	units := append(bindingFixture("painted-wolf/one", "one", "Mod+K"),
		bindingFixture("painted-wolf/two", "two", "Mod+K")...)
	faults := compileFaults(t, CompileInput{
		Units:        units,
		ProviderRank: func(string) ProviderRank { return RankStock },
	})
	if len(faults) != 2 {
		t.Fatalf("faults = %+v, want one per colliding stock declaration", faults)
	}
	for _, fault := range faults {
		if fault.Code != FaultReference || !strings.Contains(fault.Message, "collides with another stock keybinding") {
			t.Fatalf("fault = %+v", fault)
		}
	}
}

// Bundled keybindings ship live: no default may be deactivated by a tie.
func TestShippedPlatformBindingsDoNotCollide(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "contributions")
	var units []Input
	for _, kind := range Kinds() {
		entries, err := os.ReadDir(filepath.Join(dir, string(kind)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			testutil.FailErr(t, "read "+string(kind), err)
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
				continue
			}
			path := filepath.Join(dir, string(kind), entry.Name())
			body, err := os.ReadFile(path) // #nosec G304 -- bundled pack path under test
			if err != nil {
				testutil.FailErr(t, "read "+path, err)
			}
			units = append(units, Input{
				UnitID:         "contributions/" + string(kind) + "/" + strings.TrimSuffix(entry.Name(), ".yaml"),
				Kind:           kind,
				ProviderPackID: "painted-wolf/platform",
				Body:           body,
				Origin:         path,
			})
		}
	}
	if len(units) == 0 {
		t.Fatal("no shipped contribution units found; the walk is looking in the wrong place")
	}
	set, err := Compile(CompileInput{
		Units:        units,
		UnitProvider: func(string) (string, bool) { return "painted-wolf/platform", true },
		PackPresent:  func(string) bool { return true },
		ProviderRank: func(string) ProviderRank { return RankStock },
	})
	if err != nil {
		testutil.FailErr(t, "compile shipped platform contributions", err)
	}
	for _, note := range set.Notes() {
		if note.Code == NoteBindingConflict {
			t.Errorf("shipped binding conflict: %s", note.Message)
		}
	}
}

func TestBindingCrossScopeReuseIsLegal(t *testing.T) {
	one := bindingFixture("acme/one", "one", "Mod+K")
	two := []Input{
		input(KindCommand, "acme/two", "two-cmd",
			"id: acme/two:two-cmd\ntitle: T\naction: {kind: navigate, destination: home}\n"),
		input(KindKeybinding, "acme/two", "two",
			"id: acme/two:two\ncommand: acme/two:two-cmd\nscope: composer\nbindings: {macos: [Mod+K]}\n"),
	}
	set := compileWith(t, CompileInput{Units: append(one, two...)})
	active := 0
	for _, def := range set.BindingDefaults() {
		if def.Active != nil {
			active++
		}
	}
	if active != 2 {
		t.Fatalf("cross-scope reuse must keep both active: %+v", set.BindingDefaults())
	}
}

func TestConfigurationValueValidation(t *testing.T) {
	units := []Input{input(KindConfiguration, "acme/a", "depth",
		"id: acme/a:depth\ntype: number\ndescription: D.\ndefault: 2\nmin: 1\nmax: 5\n")}
	present := func(string) bool { return true }

	compileWith(t, CompileInput{Units: units, PackPresent: present,
		Configuration: map[string]map[string]any{"acme/a": {"depth": 3}}})

	fault := singleFault(t, CompileInput{Units: units, PackPresent: present,
		Configuration: map[string]map[string]any{"acme/a": {"depth": 9}}}, FaultValue)
	if !strings.Contains(fault.Message, "above max") {
		t.Fatalf("fault = %+v", fault)
	}

	singleFault(t, CompileInput{Units: units, PackPresent: present,
		Configuration: map[string]map[string]any{"acme/a": {"mystery": true}}}, FaultValue)

	// Values for an absent pack stay dormant.
	compileWith(t, CompileInput{Units: nil, PackPresent: func(string) bool { return false },
		Configuration: map[string]map[string]any{"acme/gone": {"anything": 1}}})
}

func TestConfigurationResolvesToSettingsInForce(t *testing.T) {
	units := []Input{
		input(KindConfiguration, "acme/a", "depth",
			"id: acme/a:depth\ntype: number\ndescription: D.\ndefault: 2\nmin: 1\nmax: 5\n"),
		input(KindConfiguration, "acme/a", "verbose",
			"id: acme/a:verbose\ntype: boolean\ndescription: D.\ndefault: false\n"),
		input(KindConfiguration, "other/b", "theirs",
			"id: other/b:theirs\ntype: boolean\ndescription: D.\ndefault: true\n"),
	}
	set := compileWith(t, CompileInput{
		Units:       units,
		PackPresent: func(string) bool { return true },
		Configuration: map[string]map[string]any{
			"acme/a": {"depth": 4, "verbose": true},
		},
	})

	settings := set.Settings()
	if len(settings) != 3 {
		t.Fatalf("Settings() = %d, want one per declared property", len(settings))
	}
	byID := map[string]ConfigurationSetting{}
	for _, setting := range settings {
		byID[setting.Property.ID] = setting
	}
	if depth := byID["acme/a:depth"]; depth.Value != 4 || depth.Default {
		t.Fatalf("depth = %+v, want the desired-state value", depth)
	}
	if untouched := byID["other/b:theirs"]; untouched.Value != true || !untouched.Default {
		t.Fatalf("theirs = %+v, want the declared default marked default", untouched)
	}

	// A pack reads its own values and nothing else.
	own := set.SettingsForPack("acme/a")
	if len(own) != 2 || own["depth"] != 4 || own["verbose"] != true {
		t.Fatalf("SettingsForPack = %+v", own)
	}
	if _, leaked := own["theirs"]; leaked {
		t.Fatal("a pack must not see another pack's configuration")
	}

	if !set.ConfigurationOn("acme/a:verbose") {
		t.Error("ConfigurationOn must read the value in force")
	}
	for _, absent := range []string{"acme/a:depth", "acme/a:nonesuch", "not-an-id", ""} {
		if set.ConfigurationOn(absent) {
			t.Errorf("ConfigurationOn(%q) must fail closed", absent)
		}
	}
}

// configuration_on is a real reference: it names a declared boolean property
// of the catalog, and the compiler refuses anything else.
func TestConfigurationConditionTyping(t *testing.T) {
	declarations := []Input{
		input(KindConfiguration, "acme/a", "verbose",
			"id: acme/a:verbose\ntype: boolean\ndescription: D.\ndefault: false\n"),
		input(KindConfiguration, "acme/a", "depth",
			"id: acme/a:depth\ntype: number\ndescription: D.\ndefault: 2\n"),
	}
	command := func(when string) Input {
		return input(KindCommand, "acme/a", "go",
			"id: acme/a:go\ntitle: Go\naction: {kind: navigate, destination: home}\nwhen:\n  "+when+"\n")
	}

	compileWith(t, CompileInput{
		Units:       append(append([]Input{}, declarations...), command("fact: configuration_on\n  is: acme/a:verbose")),
		PackPresent: func(string) bool { return true },
	})

	for name, when := range map[string]string{
		"undeclared":      "fact: configuration_on\n  is: acme/a:nonesuch",
		"not boolean":     "fact: configuration_on\n  is: acme/a:depth",
		"missing operand": "fact: configuration_on",
	} {
		t.Run(name, func(t *testing.T) {
			singleFault(t, CompileInput{
				Units:       append(append([]Input{}, declarations...), command(when)),
				PackPresent: func(string) bool { return true },
			}, FaultSchema)
		})
	}
}

func TestDeclarationBoundsPerKind(t *testing.T) {
	units := make([]Input, 0, MaxDeclarationsPerKind+1)
	for i := 0; i <= MaxDeclarationsPerKind; i++ {
		stem := fmt.Sprintf("cmd-%04d", i)
		units = append(units, input(KindCommand, "acme/a", stem,
			"id: acme/a:"+stem+"\ntitle: T\naction: {kind: navigate, destination: home}\n"))
	}
	fault := singleFault(t, CompileInput{Units: units}, FaultBounds)
	if fault.PackID != "acme/a" || !strings.Contains(fault.Message, "commands") {
		t.Fatalf("fault = %+v, want the pack and the kind named", fault)
	}

	// Exactly the cap compiles.
	compileWith(t, CompileInput{Units: units[:MaxDeclarationsPerKind]})
}

func TestConfigurationDefaultsTypeCheck(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"boolean default", "id: acme/a:x\ntype: boolean\ndescription: D.\ndefault: sometimes\n"},
		{"enum default outside values", "id: acme/a:x\ntype: enum\ndescription: D.\ndefault: d\nenum: [a, b]\n"},
		{"number default below min", "id: acme/a:x\ntype: number\ndescription: D.\ndefault: 0\nmin: 1\n"},
		{"missing default", "id: acme/a:x\ntype: string\ndescription: D.\n"},
		{"bounds on string", "id: acme/a:x\ntype: string\ndescription: D.\ndefault: s\nmin: 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			singleFault(t, CompileInput{Units: []Input{input(KindConfiguration, "acme/a", "x", tc.body)}}, FaultSchema)
		})
	}
}

func TestEditorActionTargetPresetCombination(t *testing.T) {
	bad := input(KindEditorAction, "acme/a", "x",
		"id: acme/a:x\ntitle: T\ntarget: {kind: selection}\nexecution: {preset: fix_finding, prompt_ref: guidance/reviewer-explain}\n")
	singleFault(t, CompileInput{Units: []Input{bad}}, FaultSchema)

	good := input(KindEditorAction, "acme/a", "x",
		"id: acme/a:x\ntitle: T\ntarget: {kind: finding, required: true}\nexecution: {preset: fix_finding, prompt_ref: guidance/reviewer-explain}\n")
	compileWith(t, CompileInput{Units: []Input{good}})
}

func TestExternalLinkRequiresHTTPS(t *testing.T) {
	unit := input(KindCommand, "acme/a", "x",
		"id: acme/a:x\ntitle: T\naction: {kind: external_link, url: \"http://example.com\"}\n")
	singleFault(t, CompileInput{Units: []Input{unit}}, FaultSchema)
}

func TestActionFieldsAreExclusive(t *testing.T) {
	unit := input(KindCommand, "acme/a", "x",
		"id: acme/a:x\ntitle: T\naction: {kind: navigate, destination: home, url: \"https://x.dev\"}\n")
	fault := singleFault(t, CompileInput{Units: []Input{unit}}, FaultSchema)
	if !strings.Contains(fault.Message, "does not take") {
		t.Fatalf("fault = %+v", fault)
	}
}

func TestCompileRejectsForeignNamespace(t *testing.T) {
	units := []Input{input(KindCommand, "acme/imposter", "explain-selection",
		"id: acme/reviewer:explain-selection\ntitle: T\naction: {kind: navigate, destination: home}\n")}
	fault := singleFault(t, CompileInput{Units: units}, FaultNamespace)
	if fault.PackID != "acme/imposter" {
		t.Fatalf("fault = %+v", fault)
	}
}

func TestCompileRejectsDuplicateAcrossUnits(t *testing.T) {
	units := []Input{
		input(KindCommand, "acme/reviewer", "explain",
			"id: acme/reviewer:explain\ntitle: T\naction: {kind: navigate, destination: home}\n"),
		input(KindEditorAction, "acme/reviewer", "explain",
			"id: acme/reviewer:explain\ntitle: T\ntarget: {kind: file}\nexecution: {preset: inspect_file, prompt_ref: guidance/reviewer-explain}\n"),
	}
	faults := compileFaults(t, CompileInput{Units: units})
	if len(faults) != 1 || faults[0].Code != FaultDuplicateID {
		t.Fatalf("faults = %+v", faults)
	}
}

func TestCompileFaultShapes(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		code string
	}{
		{"broken yaml", input(KindCommand, "acme/a", "x", "id: [unclosed\n"), FaultParse},
		{"missing id", input(KindCommand, "acme/a", "x", "title: no id\n"), FaultIDMissing},
		{"shorthand id", input(KindCommand, "acme/a", "x", "id: x\n"), FaultIDInvalid},
		{"stem mismatch", input(KindCommand, "acme/a", "x", "id: acme/a:other\n"), FaultNameMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			singleFault(t, CompileInput{Units: []Input{tc.in}}, tc.code)
		})
	}
}

func TestKindRootRoundTrip(t *testing.T) {
	for _, k := range Kinds() {
		got, ok := KindForUnitRoot(k.UnitRoot())
		if !ok || got != k {
			t.Fatalf("round trip %s → %v ok=%v", k, got, ok)
		}
	}
	if _, ok := KindForUnitRoot("contributions/unknown"); ok {
		t.Fatal("unknown root must not map to a kind")
	}
}

func TestShellFactsNeverCarryHostPlane(t *testing.T) {
	for fact, spec := range Facts() {
		if spec.Plane != PlaneHost && spec.Plane != PlaneShell {
			t.Fatalf("fact %s has no plane", fact)
		}
	}
	shellOnly := &Condition{Fact: "composer_focused"}
	if conditionUsesHostFacts(shellOnly) {
		t.Fatal("shell fact classified as host")
	}
	hostFact := &Condition{All: []Condition{{Fact: "composer_focused"}, {Fact: "project_open"}}}
	if !conditionUsesHostFacts(hostFact) {
		t.Fatal("host fact must be detected in the tree")
	}
}

// A pack reads its own settings and nobody else's.
func TestCompileRejectsConfigurationOnFromAnotherPack(t *testing.T) {
	units := []Input{
		input(KindConfiguration, "acme/other", "flag",
			"id: acme/other:flag\ntype: boolean\ndescription: A flag.\ndefault: true\nscope: [device]\n"),
		input(KindCommand, "acme/reviewer", "gated",
			"id: acme/reviewer:gated\ntitle: T\n"+
				"when: {fact: configuration_on, is: \"acme/other:flag\"}\n"+
				"action: {kind: navigate, destination: home}\n"),
	}
	fault := singleFault(t, CompileInput{Units: units}, FaultSchema)
	if fault.PackID != "acme/reviewer" || !strings.Contains(fault.Message, "acme/other") {
		t.Fatalf("fault = %+v", fault)
	}
}

func TestCompileAcceptsConfigurationOnForItsOwnProperty(t *testing.T) {
	units := []Input{
		input(KindConfiguration, "acme/reviewer", "flag",
			"id: acme/reviewer:flag\ntype: boolean\ndescription: A flag.\ndefault: true\nscope: [device]\n"),
		input(KindCommand, "acme/reviewer", "gated",
			"id: acme/reviewer:gated\ntitle: T\n"+
				"when: {fact: configuration_on, is: \"acme/reviewer:flag\"}\n"+
				"action: {kind: navigate, destination: home}\n"),
	}
	if set := compileWith(t, CompileInput{Units: units}); set.Len() != 2 {
		t.Fatalf("Len = %d want 2", set.Len())
	}
}
