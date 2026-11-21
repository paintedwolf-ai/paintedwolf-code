package profiles

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

type commandSessionFixture struct {
	posture api.SessionPosture
	mode    api.SessionMode
	modeErr error
	getErr  error
}

func (f commandSessionFixture) Get(context.Context, string) (*api.Session, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &api.Session{ID: "session", Posture: f.posture, Mode: f.resolvedMode()}, nil
}

func (f commandSessionFixture) ResolveEffectiveMode(context.Context, string) (store.RootAuthority, error) {
	if f.modeErr != nil {
		return store.RootAuthority{}, f.modeErr
	}
	return store.RootAuthority{RootSessionID: "root", Mode: f.resolvedMode()}, nil
}

func (f commandSessionFixture) resolvedMode() api.SessionMode {
	if f.mode == "" {
		return api.SessionModeAgent
	}
	return f.mode
}

func TestCommandProfilesUseActualPostureAndPreserveRetainedPolicy(t *testing.T) {
	registry, err := LoadPostureRegistry()
	testutil.FailErr(t, "load actual postures", err)
	all, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress)
	read, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	input := []sandbox.ToolProfile{{ID: "coordinator", CommandEffects: all, DenyTools: []string{"fixture-denial"}}}
	for _, posture := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureVet, api.SessionPostureBuild, api.SessionPostureOrchestrate} {
		service := New(commandSessionFixture{posture: posture}, nil, nil)
		service.SetPostureRegistry(registry)
		output := service.ConstrainCommandProfiles(t.Context(), "session", input)
		want := all
		if posture == api.SessionPostureSpec || posture == api.SessionPostureVet {
			want = read
		}
		if len(output) != 1 || output[0].CommandEffects != want || !output[0].ToolDenied("fixture-denial") {
			t.Fatalf("posture %s changed effect or retained policy: %+v", posture, output)
		}
		if input[0].CommandEffects != all {
			t.Fatal("posture mutated catalog profile")
		}
	}
	if output := (*Service)(nil).ConstrainCommandProfiles(t.Context(), "session", input); output[0].CommandEffects != 0 {
		t.Fatal("missing posture service widened native surface")
	}
}

func TestShippedReadOnlyProfilesDeclareOnlyNativeReads(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load actual profile catalog", err)
	read, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	for _, id := range []string{"worker_readonly", "explore_readonly", "plan_review_readonly"} {
		found := false
		for _, profile := range profiles {
			if profile.ID == id {
				found = true
				if profile.CommandEffects != read {
					t.Fatalf("%s permits %v", id, profile.CommandEffects.Classes())
				}
				if !profile.ToolAllowed("command") || !profile.StickyTools["command"] {
					t.Fatalf("%s does not offer sticky read-only command", id)
				}
			}
		}
		if !found {
			t.Fatalf("missing shipped profile %s", id)
		}
	}
}

func TestWorkerChildSessionPostureAndConstrainedCommandProfiles(t *testing.T) {
	registry, err := LoadPostureRegistry()
	testutil.FailErr(t, "load actual postures", err)

	read, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	readMut, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation)
	readEgress, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandNetworkEgress)
	all, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress)

	profileEffects := func(profiles []sandbox.ToolProfile, id string) toolcontract.CommandEffects {
		for _, p := range profiles {
			if p.ID == id {
				return p.CommandEffects
			}
		}
		t.Fatalf("missing profile %s", id)
		return 0
	}

	for _, parentPosture := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureVet, api.SessionPostureBuild, api.SessionPostureOrchestrate} {
		if got := store.ChildSessionPosture(parentPosture, "implementer"); got != api.SessionPostureBuild {
			t.Fatalf("implementer child posture under parent %s = %s, want build", parentPosture, got)
		}
		for _, agentType := range []string{"plan-author", "web-researcher", "path-explorer", "plan-reviewer", "general"} {
			if got := store.ChildSessionPosture(parentPosture, agentType); got != parentPosture {
				t.Fatalf("%s child posture under parent %s = %s, want %s", agentType, parentPosture, got, parentPosture)
			}
		}

		service := New(commandSessionFixture{posture: parentPosture}, nil, nil)
		service.SetPostureRegistry(registry)
		narrowed := service.ForSession(t.Context(), &api.Session{ID: "session", Posture: parentPosture})
		if len(service.ForSession(t.Context(), nil)) == 0 {
			t.Fatal("expected fallback profiles for nil session")
		}

		readOnlyPosture := parentPosture == api.SessionPostureSpec || parentPosture == api.SessionPostureVet
		if got := profileEffects(narrowed, "plan_write_only"); (readOnlyPosture && got != read) || (!readOnlyPosture && got != readMut) {
			t.Fatalf("plan_write_only under %s = %v", parentPosture, got.Classes())
		}
		if got := profileEffects(narrowed, "editor_file"); (readOnlyPosture && got != read) || (!readOnlyPosture && got != readMut) {
			t.Fatalf("editor_file under %s = %v", parentPosture, got.Classes())
		}
		if got := profileEffects(narrowed, "web_research"); (readOnlyPosture && got != read) || (!readOnlyPosture && got != readEgress) {
			t.Fatalf("web_research under %s = %v", parentPosture, got.Classes())
		}
		if got := profileEffects(narrowed, "implement"); (readOnlyPosture && got != read) || (!readOnlyPosture && got != all) {
			t.Fatalf("implement under %s = %v", parentPosture, got.Classes())
		}
		for _, roID := range []string{"worker_readonly", "explore_readonly", "plan_review_readonly"} {
			if got := profileEffects(narrowed, roID); got != read {
				t.Fatalf("%s under %s = %v, want [read_only]", roID, parentPosture, got.Classes())
			}
		}
	}
}

func TestConstrainCommandProfilesAskModeAndRoundAuthority(t *testing.T) {
	registry, err := LoadPostureRegistry()
	testutil.FailErr(t, "load actual postures", err)
	shipped, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load shipped profiles", err)

	read, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	all, _ := toolcontract.NewCommandEffects(
		toolcontract.CommandReadOnly,
		toolcontract.CommandMutation,
		toolcontract.CommandExternalExec,
		toolcontract.CommandNetworkEgress,
	)

	// Profiles with no command authority must remain at 0 when narrowed by Ask.
	input := append([]sandbox.ToolProfile(nil), shipped...)
	input = append(input, sandbox.ToolProfile{ID: "no_command_effects", CommandEffects: 0})

	for _, posture := range []api.SessionPosture{
		api.SessionPostureSpec,
		api.SessionPostureVet,
		api.SessionPostureBuild,
		api.SessionPostureOrchestrate,
	} {
		service := New(commandSessionFixture{posture: posture, mode: api.SessionModeAsk}, nil, nil)
		service.SetPostureRegistry(registry)
		narrowed := service.ConstrainCommandProfiles(t.Context(), "session", input)
		for i, p := range narrowed {
			want := input[i].CommandEffects & read
			if p.CommandEffects != want {
				t.Fatalf("posture %q profile %s effects = %v, want %v", posture, p.ID, p.CommandEffects.Classes(), want.Classes())
			}
		}
	}

	// An empty or unresolved stored posture closes the command surface (0 effects)
	// in both Agent and Ask modes rather than defaulting to Build.
	for _, mode := range []api.SessionMode{api.SessionModeAgent, api.SessionModeAsk} {
		service := New(commandSessionFixture{posture: "", mode: mode}, nil, nil)
		service.SetPostureRegistry(registry)
		narrowed := service.ConstrainCommandProfiles(t.Context(), "session", input)
		for _, p := range narrowed {
			if p.CommandEffects != 0 {
				t.Fatalf("empty posture in mode %q granted %s command effects %v, want []", mode, p.ID, p.CommandEffects.Classes())
			}
		}
	}

	// Matching RoundMode binding wins even after the store upgrades to Agent.
	upgradedStore := commandSessionFixture{posture: api.SessionPostureBuild, mode: api.SessionModeAgent}
	service := New(upgradedStore, nil, nil)
	service.SetPostureRegistry(registry)
	probe := []sandbox.ToolProfile{
		{ID: "coordinator", CommandEffects: all},
		{ID: "none", CommandEffects: 0},
	}

	askRoundCtx := hostctx.WithRoundMode(t.Context(), "session", api.SessionModeAsk)
	if got := service.ConstrainCommandProfiles(askRoundCtx, "session", probe); got[0].CommandEffects != read || got[1].CommandEffects != 0 {
		t.Fatalf("matching Ask round binding after store upgrade = %+v", got)
	}

	// Non-matching RoundMode binding falls back to ResolveEffectiveMode.
	otherRoundCtx := hostctx.WithRoundMode(t.Context(), "other-session", api.SessionModeAsk)
	if got := service.ConstrainCommandProfiles(otherRoundCtx, "session", probe); got[0].CommandEffects != all || got[1].CommandEffects != 0 {
		t.Fatalf("mismatched round binding did not fall back to store Agent mode: %+v", got)
	}
}

func TestConstrainCommandProfilesChildOfAskRootAndResolutionFailure(t *testing.T) {
	registry, err := LoadPostureRegistry()
	testutil.FailErr(t, "load actual postures", err)
	shipped, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load shipped profiles", err)
	read, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)

	mem := store.NewMemory()
	askMode := api.SessionModeAsk
	root, err := mem.Create(t.Context(), api.CreateSessionRequest{
		Mode:    &askMode,
		Posture: api.SessionPostureBuild,
	}, "proj-1")
	testutil.FailErr(t, "create ask root", err)
	child, err := mem.CreateChild(t.Context(), root, api.SpawnChildRequest{
		AgentType: "implementer",
	})
	testutil.FailErr(t, "create worker child", err)

	service := New(mem, nil, nil)
	service.SetPostureRegistry(registry)

	// Without a round binding, a child of an Ask root narrows via ResolveEffectiveMode.
	narrowed := service.ConstrainCommandProfiles(t.Context(), child.ID, shipped)
	for i, p := range narrowed {
		want := shipped[i].CommandEffects & read
		if p.CommandEffects != want {
			t.Fatalf("child profile %s under Ask root = %v, want %v", p.ID, p.CommandEffects.Classes(), want.Classes())
		}
	}

	// Inject a root-resolution failure while Get still returns the session.
	failing := New(commandSessionFixture{
		posture: api.SessionPostureBuild,
		mode:    api.SessionModeAgent,
		modeErr: store.ErrSessionModeUnavailable,
	}, nil, nil)
	failing.SetPostureRegistry(registry)
	zeroed := failing.ConstrainCommandProfiles(t.Context(), child.ID, shipped)
	for _, p := range zeroed {
		if p.CommandEffects != 0 {
			t.Fatalf("profile %s kept command effects %v on root-resolution failure", p.ID, p.CommandEffects.Classes())
		}
	}
}

func TestConstrainCommandProfilesProjectsReadOnlyCommandSchema(t *testing.T) {
	registry, err := LoadPostureRegistry()
	testutil.FailErr(t, "load actual postures", err)
	shipped, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load shipped profiles", err)
	cfg, err := toolschema.LoadSchemaDirWithOperations(filepath.Join(
		configlayout.FindModuleRoot(),
		"config", "packs", "painted-wolf", "platform", "tools", "schemas",
	))
	testutil.FailErr(t, "LoadSchemaDirWithOperations", err)
	raw, ok := cfg.ToolMeta("command")
	if !ok {
		t.Fatal("command schema missing")
	}

	service := New(commandSessionFixture{
		posture: api.SessionPostureBuild,
		mode:    api.SessionModeAsk,
	}, nil, nil)
	service.SetPostureRegistry(registry)
	narrowed := service.ConstrainCommandProfiles(t.Context(), "session", shipped)

	var coordinatorEffects toolcontract.CommandEffects
	for _, p := range narrowed {
		if p.ID == "coordinator" {
			coordinatorEffects = p.CommandEffects
			break
		}
	}
	projected := toolexecution.ProjectCommandMeta(tools.ToolMeta{
		Name:               raw.Name,
		Description:        raw.Description,
		DescriptionEffects: raw.DescriptionEffects,
		ArgsSchema:         raw.ArgsSchema,
	}, coordinatorEffects, false)

	if !slices.Equal(projected.DescriptionParts, []string{"base"}) {
		t.Fatalf("Ask projected DescriptionParts = %v, want [base]", projected.DescriptionParts)
	}
	props, ok := projected.ArgsSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("projected properties = %#v", projected.ArgsSchema["properties"])
	}
	for _, pruned := range []string{
		"capability_request",
		"secret_use",
		"snapshot_capture",
		"socks_proxy",
		"terminal_capture",
		"verification",
	} {
		if _, present := props[pruned]; present {
			t.Fatalf("Ask projected command schema still includes %q", pruned)
		}
	}
	for _, kept := range []string{"command", "cwd", "timeout_ms"} {
		if _, present := props[kept]; !present {
			t.Fatalf("Ask projected command schema missing %q", kept)
		}
	}
}
