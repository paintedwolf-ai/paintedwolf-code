package promptrender_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

type noopNudgeFormatter struct{}

func (noopNudgeFormatter) FormatNudge(_ context.Context, code string, _ map[string]any) (string, error) {
	return code, nil
}

func newTestBlockPlane(t *testing.T, s *promptrender.Stock) *toolfeedback.BlockPlane {
	t.Helper()
	repoRoot := filepath.Dir(configlayout.FindModuleRoot())
	schemaDir := filepath.Join(repoRoot, "schemas")
	if err := anchorcatalog.InstallBundled(); err != nil {
		t.Fatalf("install anchor catalog: %v", err)
	}
	if err := oar.InstallEngineDeclarationBundled(schemaDir); err != nil {
		t.Fatalf("install engine declaration: %v", err)
	}
	cfg, err := guidance.LoadHintConfigStock()
	if err != nil {
		t.Fatalf("load hint config: %v", err)
	}
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(s.Engine()))
	formatter := guidance.NewStaticRejectFormatter(cfg)
	loader, err := oar.NewLoader(schemaDir)
	if err != nil {
		t.Fatalf("new OAR loader: %v", err)
	}
	loader.SetToolSchemas(s.Schemas)
	loader.SetDetectors(oar.HostDetectors(nil))
	rs, err := loader.LoadEffectivePolicyWithCatalog(s.Catalog)
	if err != nil {
		t.Fatalf("load OAR policy: %v", err)
	}
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.SetDetectors(oar.HostDetectors(nil))
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	renderer := oar.NewRenderer(formatter, noopNudgeFormatter{})
	return &toolfeedback.BlockPlane{Pipeline: pipeline, Renderer: renderer}
}

type staticCoordFrame struct{ ctx api.CoordinatorRunContext }

func (f staticCoordFrame) BuildCoordinatorTurnFrame(context.Context, string, *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: f.ctx}, nil
}

type staticSelection struct {
	loaded map[string]bool
}

func (s staticSelection) LoadedTools(string) map[string]bool {
	out := make(map[string]bool, len(s.loaded))
	for k, v := range s.loaded {
		out[k] = v
	}
	return out
}

func (staticSelection) FullCatalog(string) bool { return false }

type staticWorkerLeg struct{ leg inject.WorkerLegContext }

func (w staticWorkerLeg) BuildWorkerPromptContext(context.Context, string, *api.Session) (inject.WorkerLegContext, error) {
	return w.leg, nil
}

func ensureWorkspaceTopologyDirs(baseDir string, rootCount int, primaryGit, anyGit bool) ([]map[string]any, int, string, int) {
	if rootCount <= 0 || strings.TrimSpace(baseDir) == "" {
		return nil, 0, "", 0
	}
	gitDir := filepath.Join(baseDir, "git-root")
	nonGitDir := filepath.Join(baseDir, "nongit-root")
	_ = os.MkdirAll(filepath.Join(gitDir, ".git"), 0o755)
	_ = os.MkdirAll(nonGitDir, 0o755)

	primaryPath := nonGitDir
	if primaryGit {
		primaryPath = gitDir
	}
	roots := []map[string]any{{
		"label":      "main",
		"path":       primaryPath,
		"is_primary": true,
		"is_active":  true,
		"has_git":    primaryGit,
	}}
	if rootCount > 1 {
		secGit := anyGit && !primaryGit
		var secPath string
		if secGit {
			secPath = gitDir
		} else {
			secAlt := filepath.Join(baseDir, "nongit-sec")
			_ = os.MkdirAll(secAlt, 0o755)
			secPath = secAlt
		}
		roots = append(roots, map[string]any{
			"label":      "secondary",
			"path":       secPath,
			"is_primary": false,
			"is_active":  false,
			"has_git":    secGit,
		})
	}
	return roots, len(roots), primaryPath, 0
}

func loadedSetFromOffered(offered []string) map[string]bool {
	m := make(map[string]bool, len(offered))
	for _, name := range offered {
		m[name] = true
	}
	return m
}

func buildRecipientEngine(
	s *promptrender.Stock,
	runCtx api.CoordinatorRunContext,
	auth hoststate.TurnAuthority,
	hasGit bool,
	jobs []bgprocess.JobSnapshot,
) *assembly.AssemblyEngine {
	pe := s.Engine()
	deps := assembly.AssemblyDeps{
		Prompts:          pe,
		Injects:          prompts.NewInjectRenderer(pe),
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: staticCoordFrame{ctx: runCtx},
		Selection:        staticSelection{loaded: loadedSetFromOffered(auth.Offered)},
		PromptToolLister: func(context.Context, *api.Session, string) ([]tools.ToolMeta, error) {
			return slices.Clone(auth.Metas), nil
		},
		WebSearchEnabled: func() bool { return true },
		WorkspaceRoots: func(_ context.Context, sess *api.Session) ([]map[string]any, int, string, int) {
			if sess == nil || strings.TrimSpace(sess.WorkspacePath) == "" {
				return nil, 0, "", 0
			}
			return ensureWorkspaceTopologyDirs(sess.WorkspacePath, 1, hasGit, hasGit)
		},
		CommandJobs: func(string) []bgprocess.JobSnapshot { return slices.Clone(jobs) },
	}
	eng := &assembly.AssemblyEngine{}
	eng.SetDeps(deps)
	return eng
}

// newlyDeliveredHostInstructions extracts only host-authored system/inject blocks
// and target-turn rejected tool guidance, excluding user content, assistant
// messages, earlier historical observations, and tool schema listings.
func newlyDeliveredHostInstructions(msgs []api.Message, targetRejectID string) []string {
	isHost := func(origin api.MessageOrigin, authority api.ContentAuthority) bool {
		return origin == api.MessageOriginHost || authority == api.ContentAuthoritySystem || authority == api.ContentAuthorityDeveloper
	}
	var out []string
	for _, m := range msgs {
		if m.ID == targetRejectID && m.ToolResult != nil && m.ToolResult.Outcome == api.ToolResultOutcomeRejected {
			out = append(out, m.Content)
			continue
		}
		if len(m.ContentParts) > 0 {
			for _, part := range m.ContentParts {
				if isHost(part.Origin, part.Authority) {
					out = append(out, part.Content)
				}
			}
			continue
		}
		if m.Role == api.MessageRoleSystem || isHost(m.Origin, m.Authority) {
			out = append(out, m.Content)
		}
	}
	return out
}

func parsedCallsForTool(cat *toolvocab.Catalog, blocks []string, wantTool string) []toolvocabtest.CallExample {
	var out []toolvocabtest.CallExample
	for _, b := range blocks {
		for _, ex := range toolvocabtest.ParseCallExamples(cat, b) {
			if ex.Tool == wantTool {
				out = append(out, ex)
			}
		}
	}
	return out
}

func assertIdenticalAuthorityAcrossTriggerFlip(t *testing.T, label string, a, b hoststate.TurnAuthority) {
	t.Helper()
	if !slices.Equal(a.Offered, b.Offered) {
		t.Fatalf("%s callable Offered changed across trigger flip: %v vs %v", label, a.Offered, b.Offered)
	}
	if !slices.Equal(a.Reader.Requestable(), b.Reader.Requestable()) {
		t.Fatalf("%s loadable Requestable changed across trigger flip: %v vs %v", label, a.Reader.Requestable(), b.Reader.Requestable())
	}
	if !reflect.DeepEqual(a.Schemas, b.Schemas) {
		t.Fatalf("%s projected Schemas changed across trigger flip", label)
	}
	for _, prog := range []string{"cat", "git", "sed", "rg", "external"} {
		if a.Reach.Program(prog) != b.Reach.Program(prog) {
			t.Fatalf("%s Program(%q) admission changed across trigger flip", label, prog)
		}
	}
}

func verifyJobTriggerRelevanceCell(
	cat *toolvocab.Catalog,
	hostBlocks []string,
	triggerPresent, routeAvailable bool,
) error {
	joined := strings.Join(hostBlocks, "\n")
	hasLiveHandlesSection := strings.Contains(joined, "## Live handles")
	calls := parsedCallsForTool(cat, hostBlocks, "command_output")
	switch {
	case !triggerPresent:
		if hasLiveHandlesSection || len(calls) > 0 {
			return fmt.Errorf("trigger=absent (routeAvailable=%t) delivered unsolicited job output guidance (section=%t, calls=%d)", routeAvailable, hasLiveHandlesSection, len(calls))
		}
	case triggerPresent && routeAvailable:
		if !hasLiveHandlesSection || len(calls) == 0 {
			return fmt.Errorf("trigger=present, route=available omitted ## Live handles or direct command_output(...) call")
		}
	case triggerPresent && !routeAvailable:
		if !hasLiveHandlesSection || !strings.Contains(joined, "handle=cmd-7") {
			return fmt.Errorf("trigger=present, route=unavailable lost job handle facts: %q", joined)
		}
		if len(calls) > 0 || strings.Contains(joined, `need="command_output"`) {
			return fmt.Errorf("trigger=present, route=unavailable leaked unavailable command_output action: %q", joined)
		}
	}
	return nil
}

func verifyArchiveOARTriggerRelevanceCell(
	cat *toolvocab.Catalog,
	hostBlocks []string,
	triggerPresent, routeAvailable bool,
) error {
	joined := strings.Join(hostBlocks, "\n")
	loaderCalls := parsedCallsForTool(cat, hostBlocks, "request_tools")
	hasCmdLoader := slices.ContainsFunc(loaderCalls, func(ex toolvocabtest.CallExample) bool {
		return strings.Contains(fmt.Sprint(ex.Args["need"]), "command")
	})
	hasArchiveContext := strings.Contains(joined, "archive inspection or extraction")
	switch {
	case !triggerPresent:
		if hasCmdLoader || hasArchiveContext || strings.Contains(joined, "vendor/sdk.7z") {
			return fmt.Errorf("trigger=absent (routeAvailable=%t) delivered archive loader advice on unrelated occurrence: %q", routeAvailable, joined)
		}
	case triggerPresent && routeAvailable:
		if !hasCmdLoader || !hasArchiveContext || !strings.Contains(joined, "vendor/sdk.7z") {
			return fmt.Errorf("trigger=present, route=available missing archive-specific request_tools(need=\"command\") loader advice: %q", joined)
		}
		if strings.Contains(joined, "unzip -p") || strings.Contains(joined, "tar -") {
			return fmt.Errorf("trigger=present, route=available (no post-load schema) leaked concrete argv: %q", joined)
		}
	case triggerPresent && !routeAvailable:
		if !strings.Contains(joined, "vendor/sdk.7z") || !strings.Contains(joined, ".7z") || !strings.Contains(joined, ".tar.gz") {
			return fmt.Errorf("trigger=present, route=unavailable lost archive rejection facts: %q", joined)
		}
		if hasCmdLoader || strings.Contains(joined, "unzip") || strings.Contains(joined, "tar -") {
			return fmt.Errorf("trigger=present, route=unavailable leaked unavailable loader/command action: %q", joined)
		}
	}
	return nil
}

// TestSeededFaultsGeneralizeAcrossPromptsOARRulesAndSkills proves the same
// claim/schema/mode protections generalize across standing prompts, OAR rules,
// and rendered skills.
func TestSeededFaultsGeneralizeAcrossPromptsOARRulesAndSkills(t *testing.T) {
	s := mustStock(t)
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	workerStates, err := hoststate.WorkerStates(t.Context())
	if err != nil {
		t.Fatalf("WorkerStates: %v", err)
	}
	buildSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
			c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded && c.RootCount == 1
	})
	var webResearchSt hoststate.WorkerState
	foundWeb := false
	for _, ws := range workerStates {
		if !ws.Excluded && ws.Overlay.ID == "stock" && ws.ToolProfile == "web_research" && ws.Posture == api.SessionPostureBuild {
			webResearchSt = ws
			foundWeb = true
			break
		}
	}
	if !foundWeb {
		t.Fatal("missing web_research WorkerState")
	}
	specSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureSpec &&
			c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
			c.RootTopology == hoststate.RootsSingleGit
	})
	buildAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), buildSt)
	webAuth, _ := hoststate.FreezeWorkerAuthority(t.Context(), webResearchSt)
	specAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), specSt)

	families := []struct {
		family      string
		labelPrefix string
		checkBT     bool
	}{
		{"prompt", "standing/implement_investigate", true},
		{"oar_rule", "oar-fix/TEST_RULE[0]", false},
		{"skill", "skill test-skill as read by coord", false},
	}

	faults := []struct {
		name    string
		id      string
		mode    api.SessionMode
		posture api.SessionPosture
		auth    hoststate.TurnAuthority
		bad     string
		valid   string
	}{
		{
			name:    "cross_tool_argument_misattribution",
			id:      buildSt.ID,
			mode:    buildSt.SessionMode,
			posture: buildSt.Posture,
			auth:    buildAuth,
			bad:     "Run `command(command=\"ls\", prompt=\"Proceed?\")`.",
			valid:   "Run `command(command=\"ls\")` and ask via `ask_user(prompt=\"Proceed?\")`.",
		},
		{
			name:    "narrowed_wait_enum_mismatch",
			id:      webResearchSt.ID,
			mode:    webResearchSt.ParentSessionMode,
			posture: webResearchSt.Posture,
			auth:    webAuth,
			bad:     "Wait via `wait(until_complete=true, conditions=[{\"kind\": \"process_done\", \"handles\": [\"cmd-1\"]}])`.",
			valid:   "Wait via `wait(until_complete=true, conditions=[{\"kind\": \"http_ready\", \"url\": \"http://127.0.0.1:8080\"}])`.",
		},
		{
			name:    "mutating_git_mode_on_readonly_posture",
			id:      specSt.ID,
			mode:    specSt.SessionMode,
			posture: specSt.Posture,
			auth:    specAuth,
			bad:     "Commit staged work with `git commit -m \"msg\" -- a.go` in `command`.",
			valid:   "Inspect status with `git status --short` in `command`.",
		},
	}

	for _, f := range faults {
		for _, fam := range families {
			t.Run(f.name+"_"+fam.family, func(t *testing.T) {
				lbl := fam.labelPrefix + " (" + f.id + ")"
				if badViols := promptrender.EvaluateClaimsForTest(s, lbl, f.bad, f.auth, f.mode, f.posture, toolvocabtest.SubjectSelfNow(), fam.checkBT); len(badViols) == 0 {
					t.Fatalf("expected seeded %s fault in %s to fail", f.name, fam.family)
				}
				if okViols := promptrender.EvaluateClaimsForTest(s, lbl, f.valid, f.auth, f.mode, f.posture, toolvocabtest.SubjectSelfNow(), fam.checkBT); len(okViols) != 0 {
					t.Fatalf("expected valid %s text in %s to pass, got %v", f.name, fam.family, okViols)
				}
			})
		}
	}
}

// TestInstructionRelevanceAcrossTriggerAndRouteMatrix implements CT05 §2a:
// paired recipient-level verification across all 4 (Trigger x Route) outcomes
// for active-job output guidance and EXTRACT_FORMAT_UNSUPPORTED OAR recovery,
// plus the 2 mandatory relevance seeded-fault witnesses (Seeds 9 & 10).
func TestInstructionRelevanceAcrossTriggerAndRouteMatrix(t *testing.T) {
	s := mustStock(t)
	bp := newTestBlockPlane(t, s)
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}

	// Pair 1: Active-job output instruction via normal AssemblyEngine.BuildCompletionMessages.
	// Route=Available uses implement_investigate all_loaded where command_output is ALREADY callable on both turns.
	// Route=Unavailable uses implement_park where command_output is neither offered nor loadable on either turn.
	availSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
			c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
			c.RootTopology == hoststate.RootsSingleGit
	})
	unavailSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
			c.TurnProfile.SurfaceID == "implement_park" && c.RootTopology == hoststate.RootsSingleGit
	})
	availAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), availSt)
	unavailAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), unavailSt)
	if !slices.Contains(availAuth.Offered, "command_output") {
		t.Fatalf("expected command_output callable in availAuth.Offered: %v", availAuth.Offered)
	}
	if slices.Contains(unavailAuth.Offered, "command_output") || slices.Contains(unavailAuth.Reader.Requestable(), "command_output") {
		t.Fatalf("expected command_output unavailable in unavailAuth: offered=%v req=%v", unavailAuth.Offered, unavailAuth.Reader.Requestable())
	}

	wsDir := t.TempDir()
	baseHistory := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "Earlier docs mentioned `command_output(handle=\"quoted-old\", cursor=0)` — inspect current build state."},
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "Checking workspace."},
		{ID: "u2", Role: api.MessageRoleUser, Content: "Continue with the check."},
	}
	liveJob := []bgprocess.JobSnapshot{{
		Handle: "cmd-7", Mode: bgprocess.JobModeBackground, OriginTool: "command",
		StartedAt: time.Now().Add(-4 * time.Second).UTC(),
	}}

	assembleJobCell := func(st hoststate.CoordinatorState, auth hoststate.TurnAuthority, jobs []bgprocess.JobSnapshot) ([]api.Message, hoststate.TurnAuthority) {
		runCtx := api.CoordinatorRunContext{
			RunID: "run-rel", WorkflowID: "implement", CurrentPhase: "work",
			PhaseCoordinatorSurface: st.TurnProfile.SurfaceID,
			PhaseHostHeld:           st.TurnProfile.SurfaceID == "await_host",
			AllowedAgents:           spawn.AmbientAllowedAgents(),
		}
		eng := buildRecipientEngine(s, runCtx, auth, true, jobs)
		sess := &api.Session{
			ID: "sess-job", AgentType: orchestration.ProfileCoordinator,
			Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild, WorkspacePath: wsDir,
		}
		eng.BeginPromptTurn(sess.ID, "")
		msgs, bErr := eng.BuildCompletionMessages(t.Context(), sess, baseHistory, nil)
		if bErr != nil {
			t.Fatalf("BuildCompletionMessages: %v", bErr)
		}
		return msgs, auth
	}

	msgsAbsAvail, authAbsAvail := assembleJobCell(availSt, availAuth, nil)
	msgsPresAvail, authPresAvail := assembleJobCell(availSt, availAuth, liveJob)
	assertIdenticalAuthorityAcrossTriggerFlip(t, "job Route=Available", authAbsAvail, authPresAvail)
	if err := verifyJobTriggerRelevanceCell(s.Names, newlyDeliveredHostInstructions(msgsAbsAvail, ""), false, true); err != nil {
		t.Fatalf("(Absent, Available) job cell: %v", err)
	}
	if err := verifyJobTriggerRelevanceCell(s.Names, newlyDeliveredHostInstructions(msgsPresAvail, ""), true, true); err != nil {
		t.Fatalf("(Present, Available) job cell: %v", err)
	}

	msgsAbsUnavail, authAbsUnavail := assembleJobCell(unavailSt, unavailAuth, nil)
	msgsPresUnavail, authPresUnavail := assembleJobCell(unavailSt, unavailAuth, liveJob)
	assertIdenticalAuthorityAcrossTriggerFlip(t, "job Route=Unavailable", authAbsUnavail, authPresUnavail)
	if err := verifyJobTriggerRelevanceCell(s.Names, newlyDeliveredHostInstructions(msgsAbsUnavail, ""), false, false); err != nil {
		t.Fatalf("(Absent, Unavailable) job cell: %v", err)
	}
	if err := verifyJobTriggerRelevanceCell(s.Names, newlyDeliveredHostInstructions(msgsPresUnavail, ""), true, false); err != nil {
		t.Fatalf("(Present, Unavailable) job cell: %v", err)
	}

	// Pair 2: EXTRACT_FORMAT_UNSUPPORTED OAR recovery instruction via BlockPlane.RejectObservation + AssemblyEngine.
	runOARMatrixAndRelevanceSeeds(t, s, bp, availSt, availAuth, wsDir, baseHistory, msgsAbsAvail, msgsPresAvail)
}

func workerDeferredCommandProfiles(base []sandbox.ToolProfile, allowDeferredCmd bool) []sandbox.ToolProfile {
	out := make([]sandbox.ToolProfile, 0, len(base))
	for _, p := range base {
		cp := p
		if cp.ID == "implement" {
			if allowDeferredCmd {
				cp.Tools = map[string]bool{"complete_leg": true, "recall": true, "scan_query": true, "request_tools": true, "command": true}
				cp.StickyTools = map[string]bool{"complete_leg": true, "recall": true, "scan_query": true, "request_tools": true}
				cp.DeferredTools = map[string]bool{"command": true}
			} else {
				cp.Tools = map[string]bool{"complete_leg": true, "recall": true, "scan_query": true}
				cp.StickyTools = map[string]bool{"complete_leg": true, "recall": true, "scan_query": true}
				cp.DeferredTools = nil
			}
		}
		out = append(out, cp)
	}
	return out
}

func buildWorkerRecipientEngine(s *promptrender.Stock, profiles []sandbox.ToolProfile, auth hoststate.TurnAuthority, listerMetas []tools.ToolMeta, hasGit bool) *assembly.AssemblyEngine {
	pe := s.Engine()
	view := &catalogview.View{
		Catalog:      s.Catalog,
		ToolProfiles: profiles,
		ToolSchemas:  s.Schemas,
	}
	deps := assembly.AssemblyDeps{
		Prompts:     pe,
		Injects:     prompts.NewInjectRenderer(pe),
		SessionView: func(context.Context, *api.Session) *catalogview.View { return view },
		Selection:   staticSelection{loaded: loadedSetFromOffered(auth.Offered)},
		Workers:     assembly.Workers{Leg: staticWorkerLeg{leg: inject.WorkerLegContext{LegID: "leg-1", Checklist: []string{"Inspect archive handling."}}}},
		Limits:      func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		PromptToolLister: func(context.Context, *api.Session, string) ([]tools.ToolMeta, error) {
			return slices.Clone(listerMetas), nil
		},
		WebSearchEnabled: func() bool { return true },
		WorkspaceRoots: func(_ context.Context, sess *api.Session) ([]map[string]any, int, string, int) {
			if sess == nil || strings.TrimSpace(sess.WorkspacePath) == "" {
				return nil, 0, "", 0
			}
			return ensureWorkspaceTopologyDirs(sess.WorkspacePath, 1, hasGit, hasGit)
		},
	}
	eng := &assembly.AssemblyEngine{}
	eng.SetDeps(deps)
	return eng
}

func runOARMatrixAndRelevanceSeeds(
	t *testing.T,
	s *promptrender.Stock,
	bp *toolfeedback.BlockPlane,
	availSt hoststate.CoordinatorState,
	availAuth hoststate.TurnAuthority,
	wsDir string,
	baseHistory, msgsJobAbsAvail, msgsJobPresAvail []api.Message,
) {
	t.Helper()
	compMeta, _ := s.Schemas.ToolMeta("complete_leg")
	recallMeta, _ := s.Schemas.ToolMeta("recall")
	scanMeta, _ := s.Schemas.ToolMeta("scan_query")
	reqMeta, _ := s.Schemas.ToolMeta("request_tools")

	// Route=Available: command is loadable with NO established post-load schema.
	availProfiles := workerDeferredCommandProfiles(s.Profiles, true)
	oarAvailOffered := []string{"complete_leg", "recall", "request_tools", "scan_query"}
	oarAvailPlan := toolsurface.Compile(oarAvailOffered, []string{"command"})
	oarAvailSchemas := map[string]map[string]any{"complete_leg": compMeta.ArgsSchema, "recall": recallMeta.ArgsSchema, "request_tools": reqMeta.ArgsSchema, "scan_query": scanMeta.ArgsSchema}
	oarAvailReach := toolreach.Freeze(oarAvailOffered, oarAvailPlan).WithProgramTable(availAuth.ProgramTable).WithSchemas(oarAvailSchemas)
	oarAvailReader, _ := prompts.NewReader(prompts.ReaderInput{
		ProfileID:   "implement",
		SessionMode: string(api.SessionModeAgent), Profiles: availProfiles,
		Plan: oarAvailPlan, Loaded: map[string]bool{"complete_leg": true, "recall": true, "request_tools": true, "scan_query": true},
		Visible: append(slices.Clone(oarAvailOffered), "command"), Schemas: s.Schemas, ProjectedSchemas: oarAvailSchemas,
		RootCount: 1, HasGit: true, HasAnyGitRoot: true, WebSearch: true,
	})
	oarAvailMetas := []tools.ToolMeta{
		{Name: "complete_leg", Description: compMeta.Description, ArgsSchema: compMeta.ArgsSchema},
		{Name: "recall", Description: recallMeta.Description, ArgsSchema: recallMeta.ArgsSchema},
		{Name: "request_tools", Description: reqMeta.Description, ArgsSchema: reqMeta.ArgsSchema},
		{Name: "scan_query", Description: scanMeta.Description, ArgsSchema: scanMeta.ArgsSchema},
	}
	oarAvailAuth := hoststate.TurnAuthority{
		Metas:        oarAvailMetas,
		Schemas:      oarAvailSchemas,
		Offered:      oarAvailOffered,
		Plan:         oarAvailPlan,
		ProgramTable: availAuth.ProgramTable,
		Reach:        oarAvailReach,
		Reader:       oarAvailReader,
		Digest:       "oar-rel-avail",
	}

	// Route=Unavailable: command is neither offered nor loadable.
	unavailProfiles := workerDeferredCommandProfiles(s.Profiles, false)
	oarUnavailOffered := []string{"complete_leg", "recall", "scan_query"}
	oarUnavailPlan := toolsurface.Compile(oarUnavailOffered, nil)
	oarUnavailSchemas := map[string]map[string]any{"complete_leg": compMeta.ArgsSchema, "recall": recallMeta.ArgsSchema, "scan_query": scanMeta.ArgsSchema}
	oarUnavailReach := toolreach.Freeze(oarUnavailOffered, oarUnavailPlan).WithSchemas(oarUnavailSchemas)
	oarUnavailReader, _ := prompts.NewReader(prompts.ReaderInput{
		ProfileID:   "implement",
		SessionMode: string(api.SessionModeAgent), Profiles: unavailProfiles,
		Plan: oarUnavailPlan, Loaded: map[string]bool{"complete_leg": true, "recall": true, "scan_query": true},
		Visible: oarUnavailOffered, Schemas: s.Schemas, ProjectedSchemas: oarUnavailSchemas,
		RootCount: 1, HasGit: true, HasAnyGitRoot: true, WebSearch: true,
	})
	oarUnavailMetas := []tools.ToolMeta{
		{Name: "complete_leg", Description: compMeta.Description, ArgsSchema: compMeta.ArgsSchema},
		{Name: "recall", Description: recallMeta.Description, ArgsSchema: recallMeta.ArgsSchema},
		{Name: "scan_query", Description: scanMeta.Description, ArgsSchema: scanMeta.ArgsSchema},
	}
	oarUnavailAuth := hoststate.TurnAuthority{
		Metas:   oarUnavailMetas,
		Schemas: oarUnavailSchemas,
		Offered: oarUnavailOffered,
		Plan:    oarUnavailPlan,
		Reach:   oarUnavailReach,
		Reader:  oarUnavailReader,
		Digest:  "oar-rel-unavail",
	}

	assembleOARCell := func(profiles []sandbox.ToolProfile, auth hoststate.TurnAuthority, listerMetas []tools.ToolMeta, tool string, rej *toolrejection.ToolReject) ([]api.Message, hoststate.TurnAuthority) {
		rErr := bp.RejectObservation(toolreach.WithRequest(t.Context(), auth.Reach), tool, "implement", nil, rej)
		if rErr == nil {
			t.Fatalf("expected rejection error for %s/%s", tool, rej.Code)
		}
		hist := append(slices.Clone(baseHistory), api.Message{
			ID: "rej-target", Role: api.MessageRoleTool, Content: rErr.Error(),
			ToolResult: &api.ToolResult{Tool: tool, Outcome: api.ToolResultOutcomeRejected},
		})
		eng := buildWorkerRecipientEngine(s, profiles, auth, listerMetas, true)
		sess := &api.Session{
			ID: "sess-oar", ParentSessionID: "coord-root", AgentType: "implementer",
			Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild, WorkspacePath: wsDir,
		}
		eng.BeginPromptTurn(sess.ID, "")
		msgs, bErr := eng.BuildCompletionMessages(t.Context(), sess, hist, nil)
		if bErr != nil {
			t.Fatalf("BuildCompletionMessages OAR: %v", bErr)
		}
		return msgs, auth
	}

	archiveRej := &toolrejection.ToolReject{
		Code: "EXTRACT_FORMAT_UNSUPPORTED",
		Data: map[string]any{
			"path":                   "vendor/sdk.7z",
			"extract_rejected_value": ".7z",
			"extract_allowed":        []string{".zip", ".tar", ".tar.gz"},
			"extract_format_cause":   "unsupported_suffix",
			"extract_invalid_format": false,
		},
	}
	unrelatedRej := &toolrejection.ToolReject{Code: "JQ_FORMAT_INVALID", Data: map[string]any{"format": "xml", "allowed": "[auto json yaml toml]"}}

	oarAvailLister := append(slices.Clone(oarAvailMetas), tools.ToolMeta{Name: "command"})
	oarAbsAvailMsgs, oarAbsAvailA := assembleOARCell(availProfiles, oarAvailAuth, oarAvailLister, "jq", unrelatedRej)
	oarPresAvailMsgs, oarPresAvailA := assembleOARCell(availProfiles, oarAvailAuth, oarAvailLister, "extract_archive", archiveRej)
	assertIdenticalAuthorityAcrossTriggerFlip(t, "OAR Route=Available", oarAbsAvailA, oarPresAvailA)
	if err := verifyArchiveOARTriggerRelevanceCell(s.Names, newlyDeliveredHostInstructions(oarAbsAvailMsgs, "rej-target"), false, true); err != nil {
		t.Fatalf("(Absent, Available) OAR cell: %v", err)
	}
	if err := verifyArchiveOARTriggerRelevanceCell(s.Names, newlyDeliveredHostInstructions(oarPresAvailMsgs, "rej-target"), true, true); err != nil {
		t.Fatalf("(Present, Available) OAR cell: %v", err)
	}

	oarAbsUnavailMsgs, oarAbsUnavailA := assembleOARCell(unavailProfiles, oarUnavailAuth, oarUnavailMetas, "jq", unrelatedRej)
	oarPresUnavailMsgs, oarPresUnavailA := assembleOARCell(unavailProfiles, oarUnavailAuth, oarUnavailMetas, "extract_archive", archiveRej)
	assertIdenticalAuthorityAcrossTriggerFlip(t, "OAR Route=Unavailable", oarAbsUnavailA, oarPresUnavailA)
	if err := verifyArchiveOARTriggerRelevanceCell(s.Names, newlyDeliveredHostInstructions(oarAbsUnavailMsgs, "rej-target"), false, false); err != nil {
		t.Fatalf("(Absent, Unavailable) OAR cell: %v", err)
	}
	if err := verifyArchiveOARTriggerRelevanceCell(s.Names, newlyDeliveredHostInstructions(oarPresUnavailMsgs, "rej-target"), true, false); err != nil {
		t.Fatalf("(Present, Unavailable) OAR cell: %v", err)
	}

	// Seed 9: Availability-only instruction in a shared section delivered on both turns.
	// Call is schema/reach-valid on both turns, caught by trigger-absent check, and cleared when gated on trigger.
	t.Run("seed_9_availability_only_instruction_in_shared_section", func(t *testing.T) {
		seededSharedSection := "Read `command_output(handle=\"<handle>\", cursor=0)` whenever `command_output` is offered."
		// Reach/schema check passes on both turns because command_output IS callable with handle and cursor!
		if v := promptrender.EvaluateClaimsForTest(s, "standing/shared", seededSharedSection, availAuth, availSt.SessionMode, availSt.Posture, toolvocabtest.SubjectSelfNow(), true); len(v) != 0 {
			t.Fatalf("expected availability-only call to be reach/schema-valid on availAuth, got %v", v)
		}
		seededAbsentBlocks := append(newlyDeliveredHostInstructions(msgsJobAbsAvail, ""), seededSharedSection)
		if err := verifyJobTriggerRelevanceCell(s.Names, seededAbsentBlocks, false, true); err == nil {
			t.Fatal("expected trigger-absent relevance check to catch availability-only instruction in shared section")
		}
		// Restoring the trigger (omitted when has_running == false, included when has_running == true) clears failure.
		if err := verifyJobTriggerRelevanceCell(s.Names, newlyDeliveredHostInstructions(msgsJobAbsAvail, ""), false, true); err != nil {
			t.Fatalf("restoring trigger failed on absent turn: %v", err)
		}
		if err := verifyJobTriggerRelevanceCell(s.Names, newlyDeliveredHostInstructions(msgsJobPresAvail, ""), true, true); err != nil {
			t.Fatalf("restoring trigger failed on present turn: %v", err)
		}
	})

	// Seed 10: Deleted legitimate instruction in the trigger-present case while negative reach scans stay clean.
	t.Run("seed_10_deleted_trigger_present_instruction", func(t *testing.T) {
		strippedJobBlocks := []string{"## Live handles\n- handle=cmd-7 mode=background tool=command elapsed=4s\nLive job output stays recorded on `<handle>`."}
		if v := promptrender.EvaluateClaimsForTest(s, "inject/command-jobs", strippedJobBlocks[0], availAuth, availSt.SessionMode, availSt.Posture, toolvocabtest.SubjectSelfNow(), true); len(v) != 0 {
			t.Fatalf("stripped job block unexpectedly failed negative reach scan: %v", v)
		}
		if err := verifyJobTriggerRelevanceCell(s.Names, strippedJobBlocks, true, true); err == nil {
			t.Fatal("expected positive trigger-present job check to catch deleted command_output instruction")
		}

		strippedOARBlocks := []string{"Code: EXTRACT_FORMAT_UNSUPPORTED\npath vendor/sdk.7z (.7z) does not have a supported archive suffix (.zip, .tar.gz)."}
		if v := promptrender.EvaluateClaimsForTest(s, "oar/EXTRACT_FORMAT_UNSUPPORTED", strippedOARBlocks[0], oarAvailAuth, api.SessionModeAgent, api.SessionPostureBuild, toolvocabtest.SubjectSelfNow(), false); len(v) != 0 {
			t.Fatalf("stripped OAR block unexpectedly failed negative reach scan: %v", v)
		}
		if err := verifyArchiveOARTriggerRelevanceCell(s.Names, strippedOARBlocks, true, true); err == nil {
			t.Fatal("expected positive trigger-present OAR check to catch deleted request_tools(need=\"command\") loader advice")
		}
	})
}

func formatCompleteCapture(title, stateID string, auth hoststate.TurnAuthority, msgs []api.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Recipient Prompt Capture: %s\n\n", title)
	fmt.Fprintf(&b, "- **State ID**: `%s`\n", stateID)
	fmt.Fprintf(&b, "- **Offered Tools (%d)**: `%s`\n", len(auth.Offered), strings.Join(auth.Offered, ", "))
	fmt.Fprintf(&b, "- **Requestable Tools (%d)**: `%s`\n\n", len(auth.Reader.Requestable()), strings.Join(auth.Reader.Requestable(), ", "))
	for i, m := range msgs {
		toolTag := ""
		if m.ToolResult != nil {
			toolTag = fmt.Sprintf(" tool=%s outcome=%s", m.ToolResult.Tool, m.ToolResult.Outcome)
		}
		fmt.Fprintf(&b, "---\n\n## Message [%d] — role=`%s` origin=`%s` authority=`%s` pinned=`%t`%s\n\n%s\n\n",
			i, m.Role, m.Origin, m.Authority, m.ContextPinned, toolTag, m.Content)
	}
	return b.String()
}

type recipientCaptureSpec struct {
	file    string
	title   string
	stateID string
	mode    api.SessionMode
	auth    hoststate.TurnAuthority
	msgs    []api.Message
}

// TestRepresentativeRecipientCompositionsDeliveryOrder implements CT05 §2b:
// assembles and verifies complete recipient prompts in delivery order across the
// 4 representative compositions, writing durable capture files when PW_CAPTURE_RECIPIENT_DIR is set.
func TestRepresentativeRecipientCompositionsDeliveryOrder(t *testing.T) {
	s := mustStock(t)
	bp := newTestBlockPlane(t, s)
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	wsDir := filepath.Join(t.TempDir(), "project")

	// 1. Sparse turn (await_host floor_only)
	sparseSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
			c.TurnProfile.SurfaceID == "await_host" && c.Loading == hoststate.LoadingFloorOnly &&
			c.RootTopology == hoststate.RootsSingleGit
	})
	sparseAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), sparseSt)
	sparseEng := buildRecipientEngine(s, api.CoordinatorRunContext{
		RunID: "run-1", WorkflowID: "implement", CurrentPhase: "await_host",
		PhaseCoordinatorSurface: "await_host", PhaseHostHeld: true,
	}, sparseAuth, true, nil)
	sparseSess := &api.Session{ID: "cap-sparse", AgentType: orchestration.ProfileCoordinator, Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild, WorkspacePath: wsDir}
	sparseEng.BeginPromptTurn(sparseSess.ID, "")
	sparseMsgs, err := sparseEng.BuildCompletionMessages(t.Context(), sparseSess, []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Check status while host step is held."}}, nil)
	if err != nil {
		t.Fatalf("sparse BuildCompletionMessages: %v", err)
	}

	// 2. Broad turn (implement_investigate all_loaded build with Git root + running job)
	broadSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
			c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
			c.RootTopology == hoststate.RootsSingleGit
	})
	broadAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), broadSt)
	broadJob := []bgprocess.JobSnapshot{{
		Handle: "cmd-7", Mode: bgprocess.JobModeBackground, OriginTool: "command",
		StartedAt: time.Now().Add(-5 * time.Second).UTC(),
	}}
	broadEng := buildRecipientEngine(s, api.CoordinatorRunContext{
		RunID: "run-2", WorkflowID: "implement", CurrentPhase: "work",
		PhaseCoordinatorSurface: "implement_investigate", AllowedAgents: spawn.AmbientAllowedAgents(),
	}, broadAuth, true, broadJob)
	broadSess := &api.Session{ID: "cap-broad", AgentType: orchestration.ProfileCoordinator, Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild, WorkspacePath: wsDir}
	broadEng.BeginPromptTurn(broadSess.ID, "")
	broadMsgs, err := broadEng.BuildCompletionMessages(t.Context(), broadSess, []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Investigate failing test and inspect running test job."}}, nil)
	if err != nil {
		t.Fatalf("broad BuildCompletionMessages: %v", err)
	}

	captures := append([]recipientCaptureSpec{
		{"01-sparse-turn.md", "Sparse Turn (await_host floor_only)", sparseSt.ID, api.SessionModeAgent, sparseAuth, sparseMsgs},
		{"02-broad-turn.md", "Broad Turn (implement_investigate all_loaded + live job)", broadSt.ID, api.SessionModeAgent, broadAuth, broadMsgs},
	}, buildLoaderAndAskTransitionCaptures(t, s, bp, coordStates, wsDir)...)

	captureDir := strings.TrimSpace(os.Getenv("PW_CAPTURE_RECIPIENT_DIR"))
	for _, capItem := range captures {
		if len(capItem.msgs) < 2 || capItem.msgs[0].Role != api.MessageRoleSystem || !capItem.msgs[0].ContextPinned {
			t.Fatalf("%s: standing system prompt must be first and ContextPinned", capItem.file)
		}
		if viols := promptrender.EvaluateClaimsForTest(s, capItem.file+":system[0]", capItem.msgs[0].Content, capItem.auth, capItem.mode, api.SessionPostureBuild, toolvocabtest.SubjectSelfNow(), true); len(viols) != 0 {
			t.Fatalf("%s standing system prompt claim violations: %v", capItem.file, viols)
		}
		body := formatCompleteCapture(capItem.title, capItem.stateID, capItem.auth, capItem.msgs)
		if testing.Verbose() {
			t.Logf("=== BEGIN_CAPTURE:%s ===\n%s\n=== END_CAPTURE:%s ===", capItem.file, body, capItem.file)
		}
		if captureDir != "" {
			if err := os.MkdirAll(captureDir, 0o755); err != nil {
				t.Fatalf("MkdirAll captureDir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(captureDir, capItem.file), []byte(body), 0o644); err != nil {
				t.Fatalf("WriteFile %s: %v", capItem.file, err)
			}
		}
	}
}

func buildLoaderAndAskTransitionCaptures(
	t *testing.T,
	s *promptrender.Stock,
	bp *toolfeedback.BlockPlane,
	coordStates []hoststate.CoordinatorState,
	wsDir string,
) []recipientCaptureSpec {
	t.Helper()
	floorSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
			c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingFloorOnly &&
			c.RootTopology == hoststate.RootsSingleGit
	})
	floorAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), floorSt)
	loaderOffered := []string{"complete_leg", "fetch_url", "recall", "record_finding", "request_decision", "request_tools", "scan_query", "web_search"}
	loaderProfiles := make([]sandbox.ToolProfile, 0, len(s.Profiles))
	for _, p := range s.Profiles {
		cp := p
		if cp.ID == "web_research" {
			cp.Tools = map[string]bool{"complete_leg": true, "fetch_url": true, "recall": true, "record_finding": true, "request_decision": true, "request_tools": true, "scan_query": true, "web_search": true, "command": true}
			cp.StickyTools = loadedSetFromOffered(loaderOffered)
			cp.DeferredTools = map[string]bool{"command": true}
		}
		loaderProfiles = append(loaderProfiles, cp)
	}
	loaderPlan := toolsurface.Compile(loaderOffered, []string{"command"})
	loaderSchemas := make(map[string]map[string]any, len(loaderOffered))
	var loaderMetas []tools.ToolMeta
	for _, tn := range loaderOffered {
		tm, _ := s.Schemas.ToolMeta(tn)
		loaderSchemas[tn] = tm.ArgsSchema
		loaderMetas = append(loaderMetas, tools.ToolMeta{Name: tn, Description: tm.Description, ArgsSchema: tm.ArgsSchema})
	}
	loaderReach := toolreach.Freeze(loaderOffered, loaderPlan).WithProgramTable(floorAuth.ProgramTable).WithSchemas(loaderSchemas)
	loaderReader, _ := prompts.NewReader(prompts.ReaderInput{
		ProfileID:   "web_research",
		SessionMode: string(api.SessionModeAgent), Profiles: loaderProfiles,
		Plan: loaderPlan, Loaded: loadedSetFromOffered(loaderOffered),
		Visible: append(slices.Clone(loaderOffered), "command"), Schemas: s.Schemas, ProjectedSchemas: loaderSchemas,
		RootCount: 1, HasGit: true, HasAnyGitRoot: true, WebSearch: true,
	})
	loaderAuth := hoststate.TurnAuthority{
		Metas:        loaderMetas,
		Schemas:      loaderSchemas,
		Offered:      loaderOffered,
		Plan:         loaderPlan,
		ProgramTable: floorAuth.ProgramTable,
		Reach:        loaderReach,
		Reader:       loaderReader,
		Digest:       "cap-loader-only",
	}
	loaderEng := buildWorkerRecipientEngine(s, loaderProfiles, loaderAuth, append(slices.Clone(loaderMetas), tools.ToolMeta{Name: "command"}), true)
	errArch := bp.RejectObservation(toolreach.WithRequest(t.Context(), loaderReach), "extract_archive", "web_research", nil,
		&toolrejection.ToolReject{
			Code: "EXTRACT_FORMAT_UNSUPPORTED",
			Data: map[string]any{
				"path":                   "vendor/sdk.7z",
				"extract_rejected_value": ".7z",
				"extract_allowed":        []string{".zip", ".tar", ".tar.gz"},
				"extract_format_cause":   "unsupported_suffix",
				"extract_invalid_format": false,
			},
		})
	errJQ := bp.RejectObservation(toolreach.WithRequest(t.Context(), loaderReach), "jq", "web_research", nil,
		&toolrejection.ToolReject{Code: "JQ_FORMAT_INVALID", Data: map[string]any{"format": "xml", "allowed": "[auto json yaml toml]"}})
	loaderSessA := &api.Session{ID: "cap-loader-pres", ParentSessionID: "coord-root", AgentType: "web-researcher", Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild, WorkspacePath: wsDir}
	loaderEng.BeginPromptTurn(loaderSessA.ID, "")
	loaderPresMsgs, lErrA := loaderEng.BuildCompletionMessages(t.Context(), loaderSessA, []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "Inspect vendor/sdk.7z."},
		{ID: "r1", Role: api.MessageRoleTool, Content: errArch.Error(), ToolResult: &api.ToolResult{Tool: "extract_archive", Outcome: api.ToolResultOutcomeRejected}},
	}, nil)
	if lErrA != nil {
		t.Fatalf("loaderPresMsgs BuildCompletionMessages: %v", lErrA)
	}
	loaderSessB := &api.Session{ID: "cap-loader-abs", ParentSessionID: "coord-root", AgentType: "web-researcher", Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild, WorkspacePath: wsDir}
	loaderEng.BeginPromptTurn(loaderSessB.ID, "")
	loaderAbsMsgs, lErrB := loaderEng.BuildCompletionMessages(t.Context(), loaderSessB, []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "Query config format."},
		{ID: "r1", Role: api.MessageRoleTool, Content: errJQ.Error(), ToolResult: &api.ToolResult{Tool: "jq", Outcome: api.ToolResultOutcomeRejected}},
	}, nil)
	if lErrB != nil {
		t.Fatalf("loaderAbsMsgs BuildCompletionMessages: %v", lErrB)
	}

	askSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return c.SessionMode == api.SessionModeAsk && c.RootTopology == hoststate.RootsSingleGit && c.Loading == hoststate.LoadingFloorOnly
	})
	askAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), askSt)
	askEng := buildRecipientEngine(s, api.CoordinatorRunContext{}, askAuth, true, nil)
	askSess := &api.Session{ID: "cap-ask", AgentType: orchestration.ProfileCoordinator, Mode: api.SessionModeAsk, Posture: api.SessionPostureBuild, WorkspacePath: wsDir}
	askEng.BeginPromptTurn(askSess.ID, "")
	errAskMut := bp.RejectObservation(toolreach.WithRequest(t.Context(), askAuth.Reach), "edit", prompts.CoordinatorProfileID, nil,
		&toolrejection.ToolReject{Code: "TOOL_COMMAND_OPERATION", Data: map[string]any{"need": "edit"}})
	askBeforeMsgs, _ := askEng.BuildCompletionMessages(t.Context(), askSess, []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "Explain how a.go works."},
		{ID: "r1", Role: api.MessageRoleTool, Content: errAskMut.Error(), ToolResult: &api.ToolResult{Tool: "edit", Outcome: api.ToolResultOutcomeRejected}},
	}, nil)

	injR := prompts.NewInjectRenderer(s.Engine())
	modeNotice, mErr := injR.RenderWithReader(t.Context(), floorAuth.Reader, "session-agent-enabled", nil)
	if mErr != nil {
		t.Fatalf("RenderWithReader session-agent-enabled: %v", mErr)
	}
	afterEng := buildRecipientEngine(s, api.CoordinatorRunContext{
		WorkflowID: "implement", CurrentPhase: "work", PhaseCoordinatorSurface: "implement_investigate",
		AllowedAgents: spawn.AmbientAllowedAgents(),
	}, floorAuth, true, nil)
	afterSess := &api.Session{ID: "cap-after", AgentType: orchestration.ProfileCoordinator, Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild, WorkspacePath: wsDir}
	afterEng.BeginPromptTurn(afterSess.ID, "")
	agentAfterMsgs, _ := afterEng.BuildCompletionMessages(t.Context(), afterSess, []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "Explain how a.go works."},
		{ID: "r1", Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Content: errAskMut.Error()},
		{ID: "sys-upgrade", Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Content: modeNotice},
		{ID: "u2", Role: api.MessageRoleUser, Content: "Now apply the fix to a.go."},
	}, nil)

	return []recipientCaptureSpec{
		{"03a-loader-recovery-trigger-present.md", "Loader Recovery Pair — Trigger Present (EXTRACT_FORMAT_UNSUPPORTED, command loadable without post-load schema)", "worker/web-researcher/loader-only/extract_format_unsupported", api.SessionModeAgent, loaderAuth, loaderPresMsgs},
		{"03b-loader-recovery-trigger-absent.md", "Loader Recovery Pair — Trigger Absent (JQ_FORMAT_INVALID, command loadable without post-load schema)", "worker/web-researcher/loader-only/jq_format_invalid", api.SessionModeAgent, loaderAuth, loaderAbsMsgs},
		{"04a-ask-before-switch.md", "Ask-to-Agent Transition — Before Switch (Ask Mode)", askSt.ID, api.SessionModeAsk, askAuth, askBeforeMsgs},
		{"04b-agent-after-switch.md", "Ask-to-Agent Transition — After Switch (Agent Mode)", floorSt.ID, api.SessionModeAgent, floorAuth, agentAfterMsgs},
	}
}
