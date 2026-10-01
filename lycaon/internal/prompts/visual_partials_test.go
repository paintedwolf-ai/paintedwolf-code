package prompts_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/testutil"
)

func allVisualCapabilityVars(vision bool) map[string]any {
	vars := map[string]any{
		"profile_has_capture_page":      true,
		"profile_has_render_view":       true,
		"profile_has_measure_page":      true,
		"profile_has_page_open":         true,
		"profile_has_page_controls":     true,
		"profile_has_page_snapshot":     true,
		"profile_has_page_act":          true,
		"profile_has_page_close":        true,
		"profile_has_terminal_open":     true,
		"profile_has_terminal_snapshot": true,
		"profile_has_terminal_capture":  true,
		"visual_show_available":         true,
		"visual_show_page":              true,
		"visual_show_terminal":          true,
		"agent_skills": listedSkills(
			"verify-visual-change", "verify-terminal-change",
		),
	}
	prompts.MergeModelCapabilityVars(vision, vars)
	return vars
}

func TestClaimEvidenceTableCoversAllRows(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()
	out, err := engine.Render(ctx, "units/claim-evidence.md", allVisualCapabilityVars(true))
	testutil.FailErr(t, "render claim-evidence full", err)

	for _, want := range []string{
		"Evidence for claims",
		"never observed runtime behavior",
		"capture_page",
		"structural",
		"filmstrip",
		"measure_page",
		"page_geometry",
		"screenshots are not measurements",
		"terminal_snapshot",
		"terminal_capture",
		"surface_snapshot{tui}",
		"piped",
		"screen evidence",
		"visual self-review",
		"verify-visual-change",
		"verify-terminal-change",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("claim-evidence full render missing %q:\n%s", want, out)
		}
	}

	// Procedure lives in the matching skill.
	for _, banned := range []string{
		"Capture binding (pick one)",
		"sandboxed file-tree probe",
		"N steps → N+1 frames",
		"frames[].evidence_handle",
		"Hermetic design kit",
		"terminal_open", "terminal_send", "terminal_read",
		"page_open", "page_act", "page_snapshot", "page_close",
		"TUI_NOT_DRIVEN",
	} {
		if strings.Contains(out, banned) {
			t.Fatalf("procedural detail %q must not appear in the claim-evidence floor", banned)
		}
	}

	// Skill pointer is not a closeout gate.
	if strings.Contains(out, "must read the listed verify-visual-change") ||
		strings.Contains(out, "required to read verify-visual-change") {
		t.Fatal("skill pointer must not be a closeout precondition")
	}
}

func TestClaimEvidenceRowsGateOnTools(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()

	codeOnly, err := engine.Render(ctx, "units/claim-evidence.md", map[string]any{"has_file_tools": true})
	testutil.FailErr(t, "render claim-evidence code-only", err)
	if !strings.Contains(codeOnly, "How code behaves") {
		t.Fatalf("code-behavior row must render without any visual tools; got %q", codeOnly)
	}
	for _, absent := range []string{"render_view", "capture_page", "measure_page", "terminal_snapshot", "Available skill"} {
		if strings.Contains(codeOnly, absent) {
			t.Fatalf("row for missing tool %q must not render without it; got %q", absent, codeOnly)
		}
	}

	captureOnly, err := engine.Render(ctx, "units/claim-evidence.md", map[string]any{
		"profile_has_capture_page": true,
	})
	testutil.FailErr(t, "render claim-evidence capture-only", err)
	if !strings.Contains(captureOnly, "capture_page") || !strings.Contains(captureOnly, "filmstrip") {
		t.Fatalf("capture rows must render with capture_page; got %q", captureOnly)
	}
	for _, absent := range []string{"Authored design intent", "measure_page", "terminal_snapshot"} {
		if strings.Contains(captureOnly, absent) {
			t.Fatalf("row for missing tool %q must not render; got %q", absent, captureOnly)
		}
	}

	terminalOnly, err := engine.Render(ctx, "units/claim-evidence.md", map[string]any{
		"profile_has_terminal_open":     true,
		"profile_has_terminal_snapshot": true,
		"agent_skills":                  listedSkills("verify-terminal-change"),
	})
	testutil.FailErr(t, "render claim-evidence terminal-only", err)
	for _, want := range []string{"terminal_snapshot", "surface_snapshot{tui}", "piped", "screen evidence", "verify-terminal-change"} {
		if !strings.Contains(terminalOnly, want) {
			t.Fatalf("terminal row missing %q; got %q", want, terminalOnly)
		}
	}
	if strings.Contains(terminalOnly, "capture_page") {
		t.Fatalf("page rows must not render terminal-only; got %q", terminalOnly)
	}

	sealedOnly, err := engine.Render(ctx, "units/claim-evidence.md", map[string]any{
		"profile_has_terminal_capture": true,
		"agent_skills":                 listedSkills("verify-terminal-change"),
	})
	testutil.FailErr(t, "render claim-evidence sealed-only", err)
	for _, want := range []string{"terminal_capture", "surface_snapshot{tui}", "flat", "verify-terminal-change"} {
		if !strings.Contains(sealedOnly, want) {
			t.Fatalf("sealed terminal row missing %q; got %q", want, sealedOnly)
		}
	}
	if strings.Contains(sealedOnly, "terminal_open") {
		t.Fatalf("sealed-only evidence row must not require a held terminal; got %q", sealedOnly)
	}

	openWithoutSnapshot, err := engine.Render(ctx, "units/claim-evidence.md", map[string]any{
		"profile_has_terminal_open": true,
	})
	testutil.FailErr(t, "render claim-evidence open-without-snapshot", err)
	if strings.Contains(openWithoutSnapshot, "terminal_snapshot") {
		t.Fatalf("terminal row needs both open and snapshot; got %q", openWithoutSnapshot)
	}
}

func TestClaimEvidenceAcceptsSourceWindowsWithoutRequiringDuplicateReads(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, available := range []bool{false, true} {
		out, err := engine.Render(context.Background(), "units/claim-evidence.md", map[string]any{
			"has_file_tools": true, "profile_has_summarize": available,
		})
		testutil.FailErr(t, "render summary source evidence", err)
		if strings.Contains(out, "`summarize` `pack.substance`") != available {
			t.Fatalf("summary source receipt availability=%t: %s", available, out)
		}
		if !strings.Contains(out, "Cite connecting call site or data transfer") || !strings.Contains(out, "alone do not prove behavior") {
			t.Fatalf("summary source receipt lost grounding limits: %s", out)
		}
	}
}

func TestClaimEvidenceSelfReviewGatesOnCapsVision(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()

	withVision := map[string]any{
		"profile_has_capture_page": true,
		"profile_has_measure_page": true,
	}
	prompts.MergeModelCapabilityVars(true, withVision)
	out, err := engine.Render(ctx, "units/claim-evidence.md", withVision)
	testutil.FailErr(t, "render with vision", err)
	if !strings.Contains(out, "visual self-review") {
		t.Fatalf("expected self-review row when caps.vision=true; got %q", out)
	}
	if !strings.Contains(out, "vision never replaces") {
		t.Fatalf("expected vision floor in self-review row; got %q", out)
	}

	visionNoMeasure := map[string]any{
		"profile_has_capture_page": true,
		"profile_has_measure_page": false,
	}
	prompts.MergeModelCapabilityVars(true, visionNoMeasure)
	out, err = engine.Render(ctx, "units/claim-evidence.md", visionNoMeasure)
	testutil.FailErr(t, "render vision without measure", err)
	if strings.Contains(out, "measure_page") {
		t.Fatalf("measure redirect must not render without measure_page; got %q", out)
	}

	noVision := map[string]any{
		"profile_has_capture_page": true,
	}
	prompts.MergeModelCapabilityVars(false, noVision)
	out, err = engine.Render(ctx, "units/claim-evidence.md", noVision)
	testutil.FailErr(t, "render without vision", err)
	if strings.Contains(out, "visual self-review") {
		t.Fatalf("self-review row must not render when caps.vision=false; got %q", out)
	}
}

func TestVisualShowTheDesignCoversUIAndTerminal(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()
	const ref = "units/visual-show-the-design.md"

	vars := allVisualCapabilityVars(false)
	both, err := engine.Render(ctx, ref, vars)
	testutil.FailErr(t, "render show partial with UI+terminal", err)
	page, err := engine.Render(ctx, "units/page-operating-procedure.md", vars)
	testutil.FailErr(t, "render offered page procedure", err)
	both += page
	for _, leak := range []string{"{{", "}}", "{%", "%}", "<nil>"} {
		if strings.Contains(both, leak) {
			t.Fatalf("template rendering leak detected %q; output:\n%s", leak, both)
		}
	}

	for _, progressRow := range []string{"Snapshot the running UI", "Snapshot the terminal"} {
		if !strings.Contains(both, progressRow) {
			t.Fatalf("guidance must specify contractual progress item %q; got:\n%s", progressRow, both)
		}
	}

	hasActiveTesting := strings.Contains(both, "exercise") || strings.Contains(both, "flow")
	hasFixMandate := strings.Contains(both, "fix") || strings.Contains(both, "defects")
	if !hasActiveTesting || !hasFixMandate {
		t.Fatalf("guidance must require actively exercising the flow and fixing defects before capture; got:\n%s", both)
	}

	expectedTools := []string{
		"capture_page", "page_open", "page_snapshot", "page_act", "page_close",
		"terminal_open", "terminal_send", "terminal_snapshot", "command",
	}
	for _, tool := range expectedTools {
		if !strings.Contains(both, "`"+tool+"`") {
			t.Fatalf("guidance missing required tool reference `%s`; got:\n%s", tool, both)
		}
	}
	if !strings.Contains(both, "terminal_capture") {
		t.Fatalf("guidance missing terminal_capture reference; got:\n%s", both)
	}

	expectedSkills := []string{"verify-visual-change", "verify-terminal-change"}
	for _, skill := range expectedSkills {
		if !strings.Contains(both, skill) {
			t.Fatalf("guidance missing required skill reference %s; got:\n%s", skill, both)
		}
	}
	if strings.Contains(both, "request_tools") {
		t.Fatalf("sticky show path must not say request_tools; got %q", both)
	}
	// This ladder covers evidence selection only.
	for _, banned := range []string{
		"artifact_ids",
		"PRESENT_MARKDOWN_EMBED",
		"![caption](artifact-uuid)",
		"proof_json.visual_artifact_ids",
	} {
		if strings.Contains(both, banned) {
			t.Fatalf("show guidance restates presentation mechanics %q; that is the closeout contract's to state", banned)
		}
	}
	terminalOnly, err := engine.Render(ctx, ref, map[string]any{
		"visual_show_available":         true,
		"visual_show_page":              false,
		"visual_show_terminal":          true,
		"profile_has_terminal_open":     true,
		"profile_has_terminal_snapshot": true,
		"agent_skills":                  listedSkills("verify-terminal-change"),
	})
	testutil.FailErr(t, "render show partial terminal-only", err)
	if !strings.Contains(terminalOnly, "Snapshot the terminal") || !strings.Contains(terminalOnly, "terminal_snapshot") {
		t.Fatalf("expected terminal show path; got %q", terminalOnly)
	}
	for _, want := range []string{"terminal_open", "terminal_send", "surface_snapshot{tui}", "verify-terminal-change"} {
		if !strings.Contains(terminalOnly, want) {
			t.Fatalf("terminal-only show path missing %q; got %q", want, terminalOnly)
		}
	}
	if strings.Contains(terminalOnly, "page_open") {
		t.Fatalf("terminal-only show path must not include page procedure; got %q", terminalOnly)
	}
	if strings.Contains(terminalOnly, "Snapshot the running UI") {
		t.Fatalf("UI show path must not render without page tools; got %q", terminalOnly)
	}

	sealedOnly, err := engine.Render(ctx, ref, map[string]any{
		"visual_show_available":        true,
		"visual_show_terminal":         true,
		"profile_has_terminal_capture": true,
		"agent_skills":                 listedSkills("verify-terminal-change"),
	})
	testutil.FailErr(t, "render show partial sealed-only", err)
	for _, want := range []string{"terminal_capture", "same call", "surface_snapshot{tui}", "never replay saved output"} {
		if !strings.Contains(sealedOnly, want) {
			t.Fatalf("sealed-only show path missing %q; got %q", want, sealedOnly)
		}
	}
	if strings.Contains(sealedOnly, "terminal_send") {
		t.Fatalf("sealed-only show path must not teach held interaction; got %q", sealedOnly)
	}

	uiOnly, err := engine.Render(ctx, ref, map[string]any{
		"visual_show_available":    true,
		"visual_show_page":         true,
		"visual_show_terminal":     false,
		"profile_has_capture_page": true,
		"profile_has_measure_page": true,
		"profile_has_page_open":    true,
		"profile_has_render_view":  true,
		"agent_skills":             listedSkills("verify-visual-change"),
	})
	testutil.FailErr(t, "render show partial UI-only", err)
	if !strings.Contains(uiOnly, "Snapshot the running UI") {
		t.Fatalf("expected UI show path with Snapshot the running UI; got %q", uiOnly)
	}
	if !strings.Contains(uiOnly, "verify-visual-change") {
		t.Fatalf("UI show path must retain verification skill discovery; got %q", uiOnly)
	}
	if strings.Contains(uiOnly, "Snapshot the terminal") {
		t.Fatalf("terminal show path must not render without terminal_snapshot; got %q", uiOnly)
	}

	deferred, err := engine.Render(ctx, ref, map[string]any{
		"visual_show_available":        true,
		"visual_show_terminal":         true,
		"visual_show_needs_request":    true,
		"profile_has_terminal_capture": true,
		"agent_skills":                 listedSkills("verify-visual-change", "verify-terminal-change"),
	})
	testutil.FailErr(t, "render show partial with deferred page capture", err)
	for _, want := range []string{"Snapshot the running UI", "Snapshot the terminal", "request_tools"} {
		if !strings.Contains(deferred, want) {
			t.Fatalf("deferred page capture path missing %q; got %q", want, deferred)
		}
	}
	if strings.Contains(deferred, "verify-visual-change") {
		t.Fatalf("deferred page capture must not teach page verification before the tools load; got %q", deferred)
	}

	none, err := engine.Render(ctx, ref, map[string]any{})
	testutil.FailErr(t, "render show partial without capture routes", err)
	if strings.TrimSpace(none) != "" {
		t.Fatalf("show guidance must stay silent without a capture route; got %q", none)
	}
}

func TestVisualShellAndOfferedProceduresKeepTerminalAndFloors(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()
	vars := allVisualCapabilityVars(true)
	vars["has_file_tools"] = true
	vars["execution_mode"] = "investigate"
	mergeUnits(t, engine, vars, promptunit.HostCoordinator, "investigate", []string{"read", "write", "command", "capture_page", "render_view", "measure_page", "page_open", "page_snapshot", "page_act", "page_close", "terminal_open", "terminal_send", "terminal_snapshot"})
	out, err := engine.Render(ctx, "agents/coordinator-core.md", vars)
	testutil.FailErr(t, "render investigate shell", err)
	page, err := engine.Render(ctx, "units/page-operating-procedure.md", vars)
	testutil.FailErr(t, "render offered page procedure", err)
	out += page
	for _, want := range []string{
		"capture_page",
		"terminal_snapshot",
		"terminal_capture",
		"verify-visual-change",
		"verify-terminal-change",
		"terminal_send",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("investigate shell missing tool/skill %q", want)
		}
	}
	if strings.Contains(out, "frames[].evidence_handle") {
		t.Fatalf("investigate shell must keep wire-field detail in the skill reference; got length %d", len(out))
	}
}

func TestInvestigateShellRendersLoadedVisualProcedure(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()
	floorVars := map[string]any{"has_file_tools": true}
	testutil.FailErr(t, "surface path vars", prompts.MergeCoordinatorSurfacePathVars("implement_investigate", nil, floorVars, prompts.SurfaceTurn{}))
	if floorVars["profile_has_capture_page"] != false || floorVars["profile_has_page_open"] != false || floorVars["profile_has_terminal_snapshot"] != false {
		t.Fatalf("unloaded visual and terminal tools stay untaught: %#v", floorVars)
	}
	sticky, err := prompts.LoadCoordinatorSurfaceFloor("implement_investigate")
	testutil.FailErr(t, "load sticky surface", err)
	deferred, err := prompts.LoadCoordinatorSurfaceLoadable("implement_investigate")
	testutil.FailErr(t, "load deferred surface", err)
	for _, name := range []string{"capture_page", "page_open", "terminal_snapshot"} {
		if slices.Contains(sticky, name) || !slices.Contains(deferred, name) {
			t.Fatalf("%s must remain loadable, not on the first-turn wire", name)
		}
	}
	vars := map[string]any{"has_file_tools": true, "agent_skills": listedSkills("verify-visual-change", "verify-terminal-change")}
	loaded := map[string]bool{"capture_page": true, "page_open": true, "page_snapshot": true, "page_act": true, "page_close": true, "measure_page": true, "terminal_open": true, "terminal_send": true, "terminal_snapshot": true, "command": true}
	testutil.FailErr(t, "surface path vars", prompts.MergeCoordinatorSurfacePathVars("implement_investigate", nil, vars, prompts.SurfaceTurn{Loaded: loaded}))
	testutil.FailErr(t, "merge coordinator prompt vars", prompts.MergeCoordinatorPromptVars("implement_investigate", prompts.ExecutionModePromptTransition{
		ExecutionMode: "investigate",
	}, prompts.CoordinatorPromptGates{}, vars))
	mergeUnits(t, engine, vars, promptunit.HostCoordinator, "investigate", nil)
	out, err := engine.Render(ctx, "agents/coordinator-core.md", vars)
	testutil.FailErr(t, "render investigate shell", err)
	for _, want := range []string{
		"Visual evidence",
		"Snapshot the running UI",
		"Snapshot the terminal",
		"surface_snapshot{tui}",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("investigate shell missing section/progress token %q", want)
		}
	}
	if strings.Contains(out, "### Deferred tools") || strings.Contains(out, "Load deferred capture tools") {
		t.Fatal("loaded tools must not be taught as deferred")
	}
	for _, section := range []string{"## Evidence\n", "### Visual evidence\n"} {
		if count := strings.Count(out, section); count != 1 {
			t.Fatalf("section %q rendered %d times, want one", section, count)
		}
	}
	plan := strings.Index(out, "## This turn")
	visual := strings.Index(out, "### Visual evidence")
	invariants := strings.Index(out, "## Invariants")
	if plan < 0 || visual < plan || invariants < visual {
		t.Fatal("capture guidance must accompany the turn plan, before general invariants")
	}
}

func TestImplementerPersonaKeepsVisualDiscoveryAndEagerTerminalProcedure(t *testing.T) {
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	withRoster := map[string]any{"agent_skills": []map[string]any{{"name": "verify-visual-change"}}}
	got, err := prompts.RenderPersona(context.Background(), engine, "implementer", withRoster)
	testutil.FailErr(t, "RenderPersona implementer", err)
	for _, want := range []string{
		"Visual evidence",
		"Snapshot the terminal",
		"terminal_send",
		"terminal_capture",
		"Evidence for claims",
		"verify-visual-change",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("implementer persona missing %q", want)
		}
	}
	if strings.Contains(got, "screenshots are not measurements") {
		t.Fatal("implementer persona teaches unloaded page tools")
	}
	// Loadable page capture is asked for by need, not taught.
	if !strings.Contains(got, "Snapshot the running UI") || !strings.Contains(got, "open the local page and capture it") {
		t.Fatal("implementer persona must ask to load page capture for a built page")
	}
	// An unloaded page tool is behind the capability map, named and taught nowhere.
	if strings.Contains(got, "page_open") || !strings.Contains(got, "browser pages") {
		t.Fatalf("page_open must stay behind the browser pages capability: %d mentions", strings.Count(got, "page_open"))
	}
	if strings.Contains(got, "verify-terminal-change") {
		t.Fatal("persona must not point at verify-terminal-change when only verify-visual-change is available")
	}

	// Once the leg loads the page tools, their guidance follows.
	withPages := map[string]any{"agent_skills": []map[string]any{{"name": "verify-visual-change"}}, "loaded_tools": map[string]bool{"capture_page": true, "page_open": true, "measure_page": true}}
	pages, err := prompts.RenderPersona(context.Background(), engine, "implementer", withPages)
	testutil.FailErr(t, "RenderPersona implementer with page tools", err)
	for _, want := range []string{"page_open", "Snapshot the running UI", "screenshots are not measurements"} {
		if !strings.Contains(pages, want) {
			t.Fatalf("implementer persona with loaded page tools missing %q", want)
		}
	}

	// Skill guidance requires a visible roster.
	bare, err := prompts.RenderPersona(context.Background(), engine, "implementer", nil)
	testutil.FailErr(t, "RenderPersona implementer without roster", err)
	for _, banned := range []string{"verify-visual-change", "verify-terminal-change"} {
		if strings.Contains(bare, banned) {
			t.Fatalf("rosterless persona points at skill %q it cannot open", banned)
		}
	}
	// The procedure the pointer supplements still has to be there.
	if !strings.Contains(bare, "Visual evidence") || !strings.Contains(bare, "Snapshot the terminal") {
		t.Fatal("gating the skill pointer must not drop the visual ladder itself")
	}
}

func TestFinalReportPresentsEveryCaptureRoute(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, capability := range []string{
		"profile_has_terminal_capture", "profile_has_terminal_snapshot",
		"profile_has_capture_page",
	} {
		t.Run(capability, func(t *testing.T) {
			out, err := engine.Render(context.Background(), "partials/coordinator-final-report.md", map[string]any{
				capability:              true,
				"visual_show_available": true,
			})
			testutil.FailErr(t, "render final report for capture route", err)
			for _, want := range []string{
				"`artifact_ids`",
				"capture",
			} {
				if !strings.Contains(out, want) {
					t.Fatalf("capture route %s missing %q", capability, want)
				}
			}
		})
	}
	out, err := engine.Render(context.Background(), "partials/coordinator-final-report.md", map[string]any{})
	testutil.FailErr(t, "render final report without capture routes", err)
	if strings.Contains(out, "final capture follows") || strings.Contains(out, "`artifact_ids`: visual UUIDs") {
		t.Fatal("unavailable capture must not introduce a completion requirement")
	}
}

func TestVisualWrapupPresentsExistingStills(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()
	out, err := engine.Render(ctx, "partials/coordinator-mode-shell-wrapup.md", map[string]any{
		"profile_has_render_view": true,
	})
	testutil.FailErr(t, "render wrapup", err)
	for _, want := range []string{
		"Present existing stills",
		"artifact_ids",
		"cannot open a running page",
		"render_view",
		"never runtime proof",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("wrapup missing %q:\n%s", want, out)
		}
	}
}

func TestVisualShellInvestigateOmitsChromeWithoutTools(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()
	out, err := engine.Render(ctx, "partials/coordinator-mode-shell-investigate.md", map[string]any{
		"has_file_tools": true,
	})
	testutil.FailErr(t, "render lean investigate shell", err)
	for _, forbid := range []string{
		"capture_page",
		"filmstrip",
		"measure_page",
		"terminal_snapshot",
		"never observed runtime behavior",
		"No secrets over the pty",
	} {
		if strings.Contains(out, forbid) {
			t.Fatalf("lean investigate shell must omit visual/terminal chrome (%q)", forbid)
		}
	}
}
