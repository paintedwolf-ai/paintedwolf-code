package inject_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestCalibratedPruningPreservesCoordinatorRequirements(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	catalog, err := renderer.UnitCatalog()
	testutil.FailErr(t, "load instruction catalog", err)
	policy, err := turnload.LoadCatalog()
	testutil.FailErr(t, "load decision release policy", err)
	guides := turnload.GuideCandidates(catalog.Units())
	scores := make(map[string]float64, len(guides))
	for _, guide := range guides {
		scores[guide.ID] = 0
	}
	engine := &decidetest.Fake{Answers: decide.Answers{
		"guides": {Kind: decide.KindMulti, Probabilities: scores, Confidence: 1},
	}}
	decision := turnload.Decide(t.Context(), engine, policy.Turn, turnload.State{User: "explain the result"}, guides)
	if decision.Abstained || len(decision.Omitted) == 0 {
		t.Fatalf("pruning did not run: %+v", decision)
	}
	omitted := make(map[string]bool, len(decision.Omitted))
	for id := range decision.Omitted {
		omitted[id] = true
	}
	selection := promptunit.Selection{Host: promptunit.HostCoordinator,
		Offered: map[string]bool{"edit": true, "update_progress": true}, Omitted: omitted}
	rendered := catalog.Rendered(selection)
	for _, id := range []string{"coordinator-progress-procedure", "visual-show-the-design"} {
		found := false
		for _, units := range rendered {
			for _, unit := range units {
				found = found || unit.ID == id
			}
		}
		if !found {
			t.Fatalf("calibrated pruning removed required unit %q", id)
		}
	}
	block, err := inject.RenderToolProceduresBlock(t.Context(), renderer, "pruned", "coordinator", []string{"edit", "update_progress"}, omitted)
	testutil.FailErr(t, "render pruned procedures", err)
	if !strings.Contains(block, "Store the checklist") || !strings.Contains(block, "PROGRESS_ITEM_NOT_CLOSED") {
		t.Fatalf("pruned surface lost progress requirements: %s", block)
	}
}

func TestToolProceduresFollowOfferedSchemas(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	for _, profile := range []string{"coordinator", "implementer"} {
		for _, tc := range []struct {
			name  string
			tools []string
			want  bool
			http  bool
			page  bool
		}{
			{name: "discovery", tools: []string{"request_tools", "read"}},
			{name: "command", tools: []string{"command"}, want: true},
			{name: "terminal", tools: []string{"terminal_open"}, want: true},
			{name: "verification", tools: []string{"verify"}, want: true},
			{name: "command and verification", tools: []string{"command", "verify"}, want: true},
			{name: "runner and page capture", tools: []string{"command", "capture_page"}, want: true},
			{name: "page capture", tools: []string{"capture_page"}, page: true},
			{name: "live page controls", tools: []string{"page_snapshot", "page_act", "page_close"}, page: true},
			{name: "page family", tools: []string{"page_open", "page_snapshot", "page_act", "page_close"}, page: true},
			{name: "measurement", tools: []string{"measure_page"}, page: true},
			{name: "mockup", tools: []string{"render_view"}, page: true},
			{name: "view image", tools: []string{"view_image"}, page: true},
			{name: "HTTP", tools: []string{"http_request"}, http: true},
			{name: "runner and HTTP", tools: []string{"command", "http_request"}, want: true, http: true},
			{name: "web research only", tools: []string{"fetch_url"}},
			{name: "narrowed", tools: []string{"command_output", "wait"}},
			{name: "no tools"},
		} {
			t.Run(profile+"/"+tc.name, func(t *testing.T) {
				block, err := inject.RenderToolProceduresBlock(t.Context(), renderer, "tool-procedures-test", profile, tc.tools, nil)
				testutil.FailErr(t, "render offered procedures", err)
				if (block != "") != (tc.want || tc.http || tc.page) {
					t.Fatalf("block = %q, want runner=%v HTTP=%v", block, tc.want, tc.http)
				}
				if tc.want && !strings.Contains(block, "`write_root`") {
					t.Fatalf("runner procedure lacks write-root capability: %q", block)
				}
				for _, field := range []string{"body_json", "cookie_jar", "response_body: discard", "reauthorizes hops"} {
					if strings.Contains(block, field) != tc.http {
						t.Fatalf("HTTP procedure field %q present=%v, want %v", field, strings.Contains(block, field), tc.http)
					}
				}
				if !tc.want && strings.Contains(block, "`write_root`") {
					t.Fatal("runner procedure leaked into a schema set without execution")
				}
				assertOfferedRunnerProcedures(t, tc.tools, block)
				assertOfferedPageProcedures(t, tc.tools, block)
			})
		}
	}
}

func assertOfferedRunnerProcedures(t *testing.T, offered []string, block string) {
	t.Helper()
	has := func(name string) bool {
		for _, tool := range offered {
			if tool == name {
				return true
			}
		}
		return false
	}
	for marker, want := range map[string]bool{
		"command(verification: true)": has("command"),
		"VERIFY_UNVERIFIABLE":         has("verify"),
		"For static page preview":     (has("command") || has("terminal_open") || has("verify")) && has("capture_page"),
	} {
		if strings.Contains(block, marker) != want {
			t.Fatalf("procedure %q present=%v, want %v for %v", marker, strings.Contains(block, marker), want, offered)
		}
	}
	for marker, capability := range map[string]toolcontract.Capability{
		"`local_listen`":     toolcontract.CapabilityLocalListen,
		"`socket_paths`":     toolcontract.CapabilitySocket,
		"`direct_ip` is for": toolcontract.CapabilityDirectIP,
	} {
		want := false
		for _, runner := range []string{"command", "verify", "terminal_open"} {
			contract, _ := toolcontract.Lookup(runner)
			want = want || has(runner) && contract.Supports(capability)
		}
		if strings.Contains(block, marker) != want {
			t.Fatalf("capability %s present=%v, want %v for %v", marker, strings.Contains(block, marker), want, offered)
		}
	}
}

func assertOfferedPageProcedures(t *testing.T, offered []string, block string) {
	t.Helper()
	for _, name := range []string{"page_open", "page_snapshot", "page_act", "page_close", "capture_page", "measure_page", "render_view", "view_image"} {
		if strings.Contains(block, "`"+name+"`") != slices.Contains(offered, name) {
			t.Fatalf("page procedure %s mismatches actual schema set %v: %s", name, offered, block)
		}
	}
}

func TestProgressProcedureFollowsCoordinatorOperations(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	for _, tool := range []string{"update_progress", "write", "edit", "replace_lines", "restore_version", "jq_edit", "task", "delegate_dispatch", "delegate_decompose", "read", "delete", "wait"} {
		for _, profile := range []string{"coordinator", "implementer"} {
			block, err := inject.RenderToolProceduresBlock(t.Context(), renderer, "progress-procedures", profile, []string{tool}, nil)
			testutil.FailErr(t, "render progress procedure", err)
			want := profile == "coordinator" && (tool == "update_progress" || progress.IsProgressGatedTool(tool))
			if strings.Contains(block, "checklist text in an assistant reply does not update it") != want {
				t.Fatalf("progress procedure present=%v, want %v for %s/%s", strings.Contains(block, "checklist text in an assistant reply does not update it"), want, profile, tool)
			}
			if want && (strings.Count(block, "Store the checklist") != 1 || !strings.Contains(block, "PROGRESS_ITEM_NOT_CLOSED") || !strings.Contains(block, "one row is sufficient for a small task")) {
				t.Fatalf("progress lifecycle contract missing or duplicated: %s", block)
			}
			if want && strings.Contains(block, "`delegate_dispatch`") != (tool == "delegate_dispatch") {
				t.Fatalf("delegate_dispatch present=%v for tool %s: %s", strings.Contains(block, "`delegate_dispatch`"), tool, block)
			}
			if want && strings.Contains(block, "`delegate_decompose`") != (tool == "delegate_decompose") {
				t.Fatalf("delegate_decompose present=%v for tool %s: %s", strings.Contains(block, "`delegate_decompose`"), tool, block)
			}
		}
	}
}
