package turnnudges

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	setupanchor "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordedKick struct {
	prompts.PromptTemplateEngine
	reader prompts.Reader
	data   map[string]any
}

func (r *recordedKick) RenderKick(_ context.Context, reader prompts.Reader, name string, data map[string]any) (string, error) {
	r.reader = reader
	r.data = data
	return name, nil
}

type failingSpendKick struct {
	prompts.PromptTemplateEngine
	called bool
}

func (r *failingSpendKick) RenderKick(context.Context, prompts.Reader, string, map[string]any) (string, error) {
	r.called = true
	return "partial", errors.New("render failed")
}

func TestFailedSpendSoftStopDoesNotDeliverPartialGuidance(t *testing.T) {
	setupanchor.Install()
	r := &failingSpendKick{}
	s := New(turnload.NewLedger(), nil)
	s.SetPrompts(r)
	n := s.SpendSoftStop(t.Context(), &api.Session{ID: "parent"})
	if !r.called || !n.Empty() {
		t.Fatalf("render called=%v nudge=%+v", r.called, n)
	}
}

type fixedSurface string

func (f fixedSurface) PromptTurnSurfaceID(string) string { return string(f) }

type fixedSpend bool

func (f fixedSpend) ClaimWarning(context.Context, *api.Session, float64) bool { return bool(f) }

type readOnlyProfiles struct {
	profiles []sandbox.ToolProfile
}

func (p readOnlyProfiles) ForSession(context.Context, *api.Session) []sandbox.ToolProfile {
	return p.profiles
}
func (p readOnlyProfiles) PromptToolProfile(_ context.Context, sess *api.Session) (string, error) {
	if sess != nil && sess.AgentType != "" {
		return prompts.ToolProfileForAgent(sess.AgentType)
	}
	return prompts.CoordinatorProfileID, nil
}
func (p readOnlyProfiles) CommandExternalHidden() bool { return false }

func TestCloseoutPreservesCancellationAndIterationContext(t *testing.T) {
	setupanchor.Install()
	r := &recordedKick{}
	s := New(turnload.NewLedger(), nil)
	s.SetPrompts(r)
	child := &api.Session{ID: "child", ParentSessionID: "parent"}
	n := s.Closeout(t.Context(), child, "implementer", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutCanceled, CancelReason: "human stopped the job"})
	if n.SignalID != string(anchor.WorkerCancelCloseout) || n.Content == "" || !r.reader.Valid() || r.data["cancel_reason"] != "human stopped the job" {
		t.Fatalf("cancellation guidance=%+v readerValid=%v data=%v", n, r.reader.Valid(), r.data)
	}
	n = s.Closeout(t.Context(), &api.Session{ID: "parent"}, "coordinator", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
	if n.SignalID != string(anchor.TurnCloseout) || n.Content == "" || !r.reader.Valid() || r.data["llm_timeout"] != true || r.data["session_command_mutation_capable"] != true {
		t.Fatalf("timeout guidance=%+v readerValid=%v data=%v", n, r.reader.Valid(), r.data)
	}
	n = s.IterationRunway(t.Context(), &api.Session{ID: "parent"}, "coordinator", 3)
	if n.SignalID != string(anchor.TurnIterationsLow) || !r.reader.Valid() || r.data["remaining"] != 3 {
		t.Fatalf("iteration guidance=%+v readerValid=%v data=%v", n, r.reader.Valid(), r.data)
	}
}

// Forced final turns read exactly what Closeout sends for their surface.
func TestForcedFinalTurnKicksReadWhatCloseoutSends(t *testing.T) {
	setupanchor.Install()
	reference := regexp.MustCompile(`(?:\{\{\s*|\{%\s*if\s+)(\w+)`)
	for _, tc := range []struct {
		stem    string
		surface string
		worker  bool
		pack    string
	}{
		{stem: "coordinator-turn-closeout", surface: "implement_dispatch", pack: "guidance/coordinator-turn-closeout.md"},
		{stem: "coordinator-closeout", surface: "implement_synthesis", pack: "guidance/coordinator-closeout.md"},
	} {
		r := &recordedKick{}
		s := New(turnload.NewLedger(), nil)
		s.SetPrompts(r)
		s.SetSurface(fixedSurface(tc.surface))
		s.Closeout(t.Context(), &api.Session{ID: "parent", WorkspacePath: "/tmp/proj"}, "coordinator", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
		raw, _, err := (prompts.BundledLayout{}).ReadBundled(tc.pack)
		testutil.FailErr(t, "read bundled template "+tc.stem, err)
		reads := map[string]bool{}
		for _, match := range reference.FindAllStringSubmatch(string(raw), -1) {
			reads[match[1]] = true
		}
		for name := range reads {
			if _, sent := r.data[name]; !sent {
				t.Errorf("%s reads %q, which Closeout does not send", tc.stem, name)
			}
		}
		for name := range r.data {
			if !reads[name] {
				t.Errorf("%s is sent %q and does not read it", tc.stem, name)
			}
		}
	}
}

// Runway and closeout nudges carry the recipient's mutation capability: the
// runway reader's write facts and the closeout's mutation-capable value follow
// the surface, session mode and worker profile.
func TestTurnNudgesCarryRecipientMutationCapability(t *testing.T) {
	setupanchor.Install()
	ledger := turnload.NewLedger()
	ledger.Activate("mut-coord", []string{"verify"}, "run verification")
	agentSess := &api.Session{ID: "mut-coord", Mode: api.SessionModeAgent, WorkspacePath: "/tmp/proj"}
	askSess := &api.Session{ID: "ro-coord", Mode: api.SessionModeAsk, WorkspacePath: "/tmp/proj"}

	service := func(surfaceID string) (*Service, *recordedKick) {
		rec := &recordedKick{}
		svc := New(ledger, fixedSpend(true))
		svc.SetPrompts(rec)
		svc.SetSurface(fixedSurface(surfaceID))
		return svc, rec
	}

	for _, tc := range []struct {
		surface string
		sess    *api.Session
		writes  bool
	}{
		{"implement_investigate", agentSess, true},
		{"observe_investigate", askSess, false},
	} {
		svc, rec := service(tc.surface)
		svc.IterationRunway(t.Context(), tc.sess, "coordinator", 2)
		iter := rec.reader.Facts()
		svc.SpendRunway(t.Context(), tc.sess, 5.0)
		spend := rec.reader.Facts()
		for name, facts := range map[string]map[string]any{"iterations": iter, "spend": spend} {
			if facts["profile_has_write_tools"] != tc.writes || facts["profile_command_mutation"] != tc.writes {
				t.Errorf("%s %s runway: profile_has_write_tools=%v profile_command_mutation=%v, want %t",
					tc.surface, name, facts["profile_has_write_tools"], facts["profile_command_mutation"], tc.writes)
			}
		}
	}

	for _, tc := range []struct {
		surface string
		sess    *api.Session
		capable bool
	}{
		{"plan_execute", agentSess, true},
		{"orchestrate_plan", agentSess, false},
		{"orchestrate_plan", askSess, false},
	} {
		svc, rec := service(tc.surface)
		svc.Closeout(t.Context(), tc.sess, "coordinator", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
		if rec.data["session_command_mutation_capable"] != tc.capable {
			t.Errorf("%s %s closeout session_command_mutation_capable=%v, want %t", tc.surface, tc.sess.Mode, rec.data["session_command_mutation_capable"], tc.capable)
		}
	}

	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load profiles", err)
	for _, tc := range []struct {
		agent, profile string
		capable        bool
	}{
		{"implementer", "implement", true},
		{"path-explorer", "explore_readonly", false},
	} {
		rec := &recordedKick{}
		svc := New(turnload.NewLedger(), nil)
		svc.SetPrompts(rec)
		svc.SetProfiles(readOnlyProfiles{profiles: profiles})
		child := &api.Session{ID: "w-" + tc.agent, ParentSessionID: "mut-coord", AgentType: tc.agent, Mode: api.SessionModeAgent, WorkspacePath: "/tmp/proj"}
		svc.Closeout(t.Context(), child, tc.profile, promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
		if rec.data["leg_command_mutation_capable"] != tc.capable {
			t.Errorf("%s worker closeout leg_command_mutation_capable=%v, want %t", tc.agent, rec.data["leg_command_mutation_capable"], tc.capable)
		}
	}
}

type staticWorkspace struct{ paths []string }

func (w staticWorkspace) Paths(context.Context, *api.Session) ([]string, error) {
	return w.paths, nil
}

type testSchemaLister struct {
	profiles []sandbox.ToolProfile
	schemas  *toolschema.Config
}

func (l testSchemaLister) List(_ context.Context, filter platform.ToolFilter) ([]tools.ToolMeta, error) {
	var prof sandbox.ToolProfile
	for _, p := range l.profiles {
		if p.ID == filter.ProfileID {
			prof = p
			break
		}
	}
	if prof.ID == "" || l.schemas == nil {
		return nil, nil
	}
	effects, effErr := surfacecatalog.AdmittedCommandEffects(l.profiles, filter.ProfileID, filter.SurfaceID)
	mutEffect, _ := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	cmdReadOnly := effErr != nil || !effects.Allows(mutEffect)
	var names []string
	for name := range l.schemas.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []tools.ToolMeta
	for _, name := range names {
		if !prof.ToolAllowed(name) || (filter.ProjectRootCount == 0 && toolscope.RequiresProjectRoots(name)) {
			continue
		}
		declared, ok := l.schemas.ToolMeta(name)
		if !ok {
			continue
		}
		out = append(out, tools.ToolMeta{
			Name:        name,
			Description: declared.Description,
			ArgsSchema: toolschema.ProjectToolArgsSchema(name, declared.ArgsSchema, toolschema.ProjectOptions{
				CommandReadOnly:     name == "command" && cmdReadOnly,
				PruneWaitConditions: name == "wait",
				WaitConditions:      prof.WaitConditions,
			}),
			Deferred: prof.ToolDeferred(name),
		})
	}
	return out, nil
}

type liveTurnProjector struct {
	deps promptloop.ContextDeps
}

func (p liveTurnProjector) PromptTurnSurfaceID(string) string { return "" }

func (p liveTurnProjector) ProjectTurnTools(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) ([]tools.ToolMeta, toolsurface.Plan, string, error) {
	return promptloop.ProjectTurnTools(ctx, p.deps, sess, history, frame)
}

func TestTurnNudgesProjectionMatchesTargetRequestAcrossHostAxes(t *testing.T) {
	setupanchor.Install()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "CatalogForConsumers", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "LoadEffectiveToolSchemas", err)

	nonGitDir := t.TempDir()
	ledger := turnload.NewLedger()

	buildDeps := func(webSearch bool, commandJobs bool, customProfiles []sandbox.ToolProfile) promptloop.ContextDeps {
		lister := testSchemaLister{profiles: customProfiles, schemas: schemas}
		return promptloop.ContextDeps{
			Policy: toolpolicy.NewEngine(toolpolicy.EngineDeps{
				ToolLister:       lister,
				ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
			}),
			LoadedTools:      func(id string) map[string]bool { return ledger.Active(id) },
			ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
			LiveResources: func(string) toolcontract.ResourcePresence {
				return toolcontract.ResourcePresence{CommandJobs: commandJobs}
			},
			ContextSurfaceDeps: promptloop.ContextSurfaceDeps{
				WebSearchEnabled: func() bool { return webSearch },
			},
		}
	}

	t.Run("non-git root and disabled web search match target request", func(t *testing.T) {
		pDeps := buildDeps(false, false, profiles)
		rec := &recordedKick{}
		svc := New(ledger, fixedSpend(true))
		svc.SetPrompts(rec)
		svc.SetWorkspace(staticWorkspace{paths: []string{nonGitDir}})
		svc.SetWebSearch(func() bool { return false })
		svc.SetSurface(liveTurnProjector{deps: pDeps})

		sess := &api.Session{ID: "non-git-no-web", Mode: api.SessionModeAgent, WorkspacePath: nonGitDir}
		targetMetas, _, _, err := promptloop.ProjectTurnTools(t.Context(), pDeps, sess, nil, inject.CoordinatorTurnFrame{})
		testutil.FailErr(t, "ProjectTurnTools", err)
		var wantOffered []string
		for _, meta := range targetMetas {
			wantOffered = append(wantOffered, meta.Name)
		}
		sort.Strings(wantOffered)

		_ = svc.IterationRunway(t.Context(), sess, "coordinator", 2)
		if !rec.reader.Valid() {
			t.Fatal("expected valid reader for non-Git root")
		}
		if !reflect.DeepEqual(rec.reader.Offered(), wantOffered) {
			t.Fatalf("offered = %v, want target request %v", rec.reader.Offered(), wantOffered)
		}
		facts := rec.reader.Facts()
		if facts["has_git"] != false || facts["has_any_git_root"] != false || facts["can_git_read"] != false || facts["can_git_commit"] != false {
			t.Fatalf("non-Git root falsely claimed Git facts: has_git=%v has_any_git_root=%v can_git_read=%v can_git_commit=%v",
				facts["has_git"], facts["has_any_git_root"], facts["can_git_read"], facts["can_git_commit"])
		}
		if facts["web_search_enabled"] != false || facts["profile_has_web_search"] == true || facts["profile_has_fetch_url"] == true {
			t.Fatalf("disabled web search leaked research tools: web_search_enabled=%v offered=%v", facts["web_search_enabled"], rec.reader.Offered())
		}

		// Pre-closeout recovery subject preserves mutating command capability while closeout itself offers no tools.
		_ = svc.Closeout(t.Context(), sess, "coordinator", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
		if rec.data["session_command_mutation_capable"] != true {
			t.Fatalf("expected pre-closeout session_command_mutation_capable=true on non-Git mutating surface, got %v", rec.data["session_command_mutation_capable"])
		}
		if len(rec.reader.Offered()) != 0 || rec.reader.Facts()["has_git"] != false || rec.reader.Facts()["web_search_enabled"] != false {
			t.Fatalf("closeout reader = offered:%v has_git:%v web_search:%v", rec.reader.Offered(), rec.reader.Facts()["has_git"], rec.reader.Facts()["web_search_enabled"])
		}
	})

	t.Run("active job promotion and narrowed wait match target request", func(t *testing.T) {
		narrowed := make([]sandbox.ToolProfile, len(profiles))
		copy(narrowed, profiles)
		for i := range narrowed {
			if narrowed[i].ID == prompts.CoordinatorProfileID {
				narrowed[i].WaitConditions = []string{"next_worker_done"}
			}
		}
		pDeps := buildDeps(true, true, narrowed)
		rec := &recordedKick{}
		svc := New(ledger, fixedSpend(true))
		svc.SetPrompts(rec)
		svc.SetProfiles(readOnlyProfiles{profiles: narrowed})
		svc.SetWorkspace(staticWorkspace{paths: []string{nonGitDir}})
		svc.SetSurface(liveTurnProjector{deps: pDeps})

		sess := &api.Session{ID: "job-promoted", Mode: api.SessionModeAgent, WorkspacePath: nonGitDir}
		targetMetas, _, _, err := promptloop.ProjectTurnTools(t.Context(), pDeps, sess, nil, inject.CoordinatorTurnFrame{})
		testutil.FailErr(t, "ProjectTurnTools promoted", err)
		var wantOffered []string
		for _, meta := range targetMetas {
			wantOffered = append(wantOffered, meta.Name)
		}
		sort.Strings(wantOffered)

		_ = svc.SpendRunway(t.Context(), sess, 10.0)
		if !reflect.DeepEqual(rec.reader.Offered(), wantOffered) {
			t.Fatalf("SpendRunway offered = %v, want promoted target request %v", rec.reader.Offered(), wantOffered)
		}
		if rec.reader.Facts()["profile_has_command_output"] != true || rec.reader.Facts()["profile_has_command_stop"] != true {
			t.Fatalf("active-job promotion missing command_output/command_stop on turnnudges reader: %v", rec.reader.Offered())
		}
	})

	t.Run("coordinator surface compile failure fails closed without profile fallback", func(t *testing.T) {
		svc := New(ledger, fixedSpend(true))
		svc.SetSurface(fixedSurface("nonexistent_surface"))
		sess := &api.Session{ID: "bad-surface", Mode: api.SessionModeAgent, WorkspacePath: nonGitDir}
		if r := svc.activeTurnReader(t.Context(), sess, "coordinator"); r.Valid() {
			t.Fatalf("expected invalid Reader when coordinator surface compilation fails, got offered=%v", r.Offered())
		}
	})
}
