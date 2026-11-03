package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

func newSkillsTestManager(t *testing.T) *Host {
	t.Helper()
	return NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
}

func writeSkill(t *testing.T, root, rel, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rel), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
}

func TestEffectiveSkillsGateMatrix(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeSkill(t, root, settingsoverlay.DirName()+"/skills", "house-style", "House rules for this project.")

	cases := []struct {
		name      string
		deviceOn  bool
		projectOn bool
		wantSkill bool
	}{
		{"device_off", false, true, false},
		{"project_off", true, false, false},
		{"all_on", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newSkillsTestManager(t)
			surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
			testutil.FailErr(t, "surfaces", err)
			if err := surfaces.PutEnabled(map[string]bool{projectcontrib.SurfaceSkills: tc.deviceOn}); err != nil {
				testutil.FailErr(t, "PutEnabled", err)
			}
			project.SetDefaultOpenPolicy(project.TestOpenPolicy())
			reg := project.NewMemoryRegistry()
			p, err := project.CreateWithRoot(ctx, reg, root)
			testutil.FailErr(t, "CreateWithRoot", err)
			if !tc.projectOn {
				if _, err := reg.SetTrustEnabled(ctx, p.ID, map[string]bool{projectcontrib.SurfaceSkills: false}); err != nil {
					testutil.FailErr(t, "SetTrustEnabled", err)
				}
			}
			m.SetProjectRegistry(reg)
			m.SetEffectiveCatalogDeps("", extpacks.Active(), surfaces)
			m.Profiles.SetSkillsGate(&settings.ProjectSurfaceGate{
				Surface:  projectcontrib.SurfaceSkills,
				Surfaces: surfaces,
				Projects: reg,
			})

			loaded, _ := m.Profiles.EffectiveSkills(ctx, p.ID, []string{root})
			found := false
			for _, sk := range loaded {
				if sk.Name == "house-style" && sk.Project {
					found = true
				}
			}
			if found != tc.wantSkill {
				t.Fatalf("project skill present=%v want %v", found, tc.wantSkill)
			}
		})
	}
}

func TestEffectiveSkillsGateOffReadsNothing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	lycaon := filepath.Join(root, settingsoverlay.DirName(), "skills")
	agents := filepath.Join(root, ".agents", "skills")
	if err := os.MkdirAll(lycaon, 0o755); err != nil {
		testutil.FailErr(t, "mkdir lycaon", err)
	}
	if err := os.MkdirAll(agents, 0o755); err != nil {
		testutil.FailErr(t, "mkdir agents", err)
	}
	if err := os.Chmod(lycaon, 0o000); err != nil {
		testutil.FailErr(t, "chmod lycaon", err)
	}
	if err := os.Chmod(agents, 0o000); err != nil {
		testutil.FailErr(t, "chmod agents", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(lycaon, 0o755)
		_ = os.Chmod(agents, 0o755)
	})

	m := newSkillsTestManager(t)
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
	testutil.FailErr(t, "surfaces", err)
	if err := surfaces.PutEnabled(map[string]bool{projectcontrib.SurfaceSkills: false}); err != nil {
		testutil.FailErr(t, "PutEnabled", err)
	}
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, root)
	testutil.FailErr(t, "CreateWithRoot", err)
	m.SetProjectRegistry(reg)
	m.SetEffectiveCatalogDeps("", extpacks.Active(), surfaces)
	m.Profiles.SetSkillsGate(&settings.ProjectSurfaceGate{
		Surface:  projectcontrib.SurfaceSkills,
		Surfaces: surfaces,
		Projects: reg,
	})

	// With the gate off, DiscoverProject is never called.
	_, diags := m.Profiles.EffectiveSkills(ctx, p.ID, []string{root})
	for _, d := range diags {
		if strings.Contains(d.Message, "approval") || strings.Contains(d.Code, "path") {
			t.Fatalf("unexpected disk diagnostic with gate off: %+v", d)
		}
	}
}

func TestEffectiveSkillsUnwiredGateClosed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeSkill(t, root, settingsoverlay.DirName()+"/skills", "house-style", "House rules.")
	m := newSkillsTestManager(t)
	// No SetSkillsGate.
	loaded, _ := m.Profiles.EffectiveSkills(ctx, "any", []string{root})
	for _, sk := range loaded {
		if sk.Project {
			t.Fatalf("unwired gate must not load project skills: %#v", sk)
		}
	}
}

func TestEffectiveSkillsShadowsStock(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeSkill(t, root, settingsoverlay.DirName()+"/skills", "verify-a-change", "Project attempt to replace stock.")

	m := newSkillsTestManager(t)
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
	testutil.FailErr(t, "surfaces", err)
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, root)
	testutil.FailErr(t, "CreateWithRoot", err)
	m.SetProjectRegistry(reg)
	moduleRoot := filepath.Join("..", "..")
	m.SetEffectiveCatalogDeps(moduleRoot, nil, surfaces)
	m.Profiles.SetSkillsGate(&settings.ProjectSurfaceGate{
		Surface:  projectcontrib.SurfaceSkills,
		Surfaces: surfaces,
		Projects: reg,
	})

	loaded, diags := m.Profiles.EffectiveSkills(ctx, p.ID, []string{root})
	var verify *skills.Skill
	for i := range loaded {
		if loaded[i].Name == "verify-a-change" {
			verify = &loaded[i]
		}
	}
	if verify == nil {
		t.Fatalf("stock skill missing; diags=%v", diags)
	}
	if verify.Project {
		t.Fatal("device skill must win over project shadow")
	}
	if strings.Contains(verify.Description, "Project attempt") {
		t.Fatal("project body must not replace stock")
	}
	found := false
	for _, d := range diags {
		if d.Code == extpacks.DiagSkillShadowed && d.UnitID == "skills/verify-a-change" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want skill_shadowed; diags=%v", diags)
	}
}

func TestEffectiveSkillsNoTaint(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeSkill(t, root, ".agents/skills", "release-check", "Release checklist.")

	mem := store.NewMemory()
	m := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
	testutil.FailErr(t, "surfaces", err)
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, root)
	testutil.FailErr(t, "CreateWithRoot", err)
	m.SetProjectRegistry(reg)
	m.SetEffectiveCatalogDeps("", extpacks.Active(), surfaces)
	m.Profiles.SetSkillsGate(&settings.ProjectSurfaceGate{
		Surface:  projectcontrib.SurfaceSkills,
		Surfaces: surfaces,
		Projects: reg,
	})

	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create session", err)

	loaded, _ := m.Profiles.EffectiveSkills(ctx, p.ID, []string{root})
	found := false
	for _, sk := range loaded {
		if sk.Name == "release-check" && sk.Project {
			found = true
		}
	}
	if !found {
		t.Fatal("expected project skill")
	}
	if mem.SessionUntrustedContent(sess.ID) {
		t.Fatal("loading a project skill must not taint the session")
	}
}

func TestDiagSkillShadowedRegistered(t *testing.T) {
	for _, c := range extpacks.AllDiagnosticCodes() {
		if c == extpacks.DiagSkillShadowed {
			return
		}
	}
	t.Fatal("DiagSkillShadowed missing from registry")
}

func TestTerminalEmptyResponseRendersContextualOAR(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	mgr.ToolPolicy.Pipeline.EnableAnchor(oar.AnchorContentOutput)
	for mask := 0; mask < 8; mask++ {
		obs := oar.ContentObservation{}
		if mask&1 != 0 {
			obs.EmptyResponse = &oar.EmptyResponseObservation{ToolsCallable: mask&2 != 0, StructuredOutput: mask&4 != 0}
		}
		reject, blocked, _, _, err := mgr.ToolPolicy.ContentBlock(t.Context(), &api.Session{ID: "empty-response"}, oar.AnchorContentOutput, obs, "", nil)
		testutil.FailErr(t, "evaluate terminal-empty policy", err)
		if blocked != (mask&1 != 0) {
			t.Fatalf("mask%d: blocked=%v", mask, blocked)
		}
		if !blocked {
			continue
		}
		if reject.Code() != "MODEL_RESPONSE_EMPTY" {
			t.Fatalf("wrong rejection: %+v", reject)
		}
		for _, clause := range []struct {
			text string
			want bool
		}{
			{"structured answer", mask&4 != 0},
			{"submit a native call", mask&4 == 0 && mask&2 != 0},
			{"Tool calls are unavailable", mask&4 == 0 && mask&2 == 0},
		} {
			if strings.Contains(reject.Body, clause.text) != clause.want {
				t.Fatalf("mask%d: clause %q mismatch: %s", mask, clause.text, reject.Body)
			}
		}
	}
}

// The broker reads the invocation's scratch from the ask, the fact its
// confinement carries: its own scratch is already in bounds, and any other
// session's scratch is control plane, which never raises a card.
func TestWriteRootBrokerReadsScratchFromTheAsk(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	own := enginepaths.SessionScratchUnder(cfg, "chat-own")
	worker := enginepaths.SessionScratchUnder(cfg, "worker-1")
	for _, dir := range []string{own, worker} {
		testutil.FailErr(t, "create scratch "+dir, os.MkdirAll(dir, 0o700))
	}
	broker := &WriteRootCheckpointBroker{
		Checkpoints: unusedWriteRootCheckpoints{t: t},
		Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
	}
	ask := func(proposed, scratch string) command.SandboxWriteRootResult {
		t.Helper()
		got, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
			SessionID: "chat-own", ToolName: "command", ProposedWriteRoot: proposed, SessionScratchRoot: scratch,
		})
		testutil.FailErr(t, "authorize "+proposed, err)
		return got
	}

	if got := ask(filepath.Join(own, "build"), own); !got.Authorized || got.Raised {
		t.Fatalf("own scratch: %+v, want authorized without a card", got)
	}
	if got := ask(filepath.Join(worker, "build"), own); got.Authorized || got.Raised {
		t.Fatalf("another session's scratch: %+v, want a silent refusal", got)
	}
	if got := ask(filepath.Join(own, "build"), ""); got.Authorized || got.Raised {
		t.Fatalf("scratch without the invocation's scratch root: %+v, want a silent refusal", got)
	}
}

func TestWriteRootAndReadPathBrokerResults(t *testing.T) {
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	broker := &WriteRootCheckpointBroker{
		Runtime:           runtime,
		ReadRuntime:       runtime,
		Checkpoints:       unusedWriteRootCheckpoints{t: t},
		ApprovalsDisabled: func(string) bool { return true },
	}
	ctx := t.Context()
	projectDir := t.TempDir()

	// 1. InRoots ordinary subject (sandbox_write_root_broker.go line 99)
	resInRoots, err := broker.Authorize(ctx, command.SandboxWriteRootAsk{
		SessionID: "sess", ProjectDir: projectDir, ProposedWriteRoot: filepath.Join(projectDir, "sub"),
	})
	testutil.FailErr(t, "Authorize in roots", err)
	if !resInRoots.Authorized {
		t.Fatalf("resInRoots = %+v, want authorized", resInRoots)
	}

	// 2. ApprovalsDisabled autoGrant (sandbox_write_root_broker.go line 154)
	outsideRoot := filepath.Join(t.TempDir(), "outside")
	resAuto, err := broker.Authorize(ctx, command.SandboxWriteRootAsk{
		SessionID: "sess", ProjectDir: projectDir, ProposedWriteRoot: outsideRoot,
	})
	testutil.FailErr(t, "Authorize approvals disabled", err)
	if !resAuto.Authorized {
		t.Fatalf("resAuto = %+v, want authorized", resAuto)
	}

	// 3. Exact overlay match for non-ordinary subject (sandbox_write_root_broker.go line 102)
	exactPath := filepath.Join(t.TempDir(), ".ssh", "id_ed25519")
	runtime.GrantSessionWriteRoot("sess", exactPath)
	resExact, err := broker.Authorize(ctx, command.SandboxWriteRootAsk{
		SessionID: "sess", ProjectDir: projectDir, ProposedWriteRoot: exactPath,
	})
	testutil.FailErr(t, "Authorize exact non-ordinary", err)
	if !resExact.Authorized {
		t.Fatalf("resExact = %+v, want authorized", resExact)
	}

	// 4. Rule deny (sandbox_write_root_broker.go line 110)
	broker.ApprovalsDisabled = nil
	broker.Rule = func(context.Context, string, string, string) (settings.ApprovalRule, bool) {
		return settings.ApprovalRule{Effect: settings.ApprovalEffectDeny}, true
	}
	resDeny, err := broker.Authorize(ctx, command.SandboxWriteRootAsk{
		SessionID: "sess", ProjectDir: projectDir, ProposedWriteRoot: "/opt/paintedwolf-broker-test-nonexistent/denied",
	})
	testutil.FailErr(t, "Authorize rule deny", err)
	if !resDeny.Denied {
		t.Fatalf("resDeny = %+v, want denied", resDeny)
	}

	// 5. AuthorizeRead autoGrant (sandbox_read_path_broker.go line 69)
	broker.Rule = nil
	broker.ApprovalsDisabled = func(string) bool { return true }
	readRes, err := broker.AuthorizeRead(ctx, command.SandboxReadPathAsk{
		SessionID: "sess", ProjectDir: projectDir, ProposedReadPath: filepath.Join(t.TempDir(), "read_any"),
	})
	testutil.FailErr(t, "AuthorizeRead", err)
	if !readRes.Authorized {
		t.Fatalf("readRes = %+v, want authorized", readRes)
	}
}
