package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settings/settingstest"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWriteRootPlanBindsProjectIDOnDurableGrants(t *testing.T) {
	t.Parallel()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	authority := settings.NewRuleApprovalGate(store, settingstest.EmptySources())
	broker := &WriteRootCheckpointBroker{
		Runtime:   approvalstate.NewSandboxPathGrantRuntime(),
		Authority: authority,
	}
	projectID := "proj-write-root"
	projectDir := t.TempDir()
	proposed := "/opt/lycaon-broker-test-nonexistent/buildx"
	_, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn,
		Ran:   gate.ProducerFilePath | gate.ProducerLease,
		File: &gate.FileTarget{
			Path: proposed, Mode: gate.ModeWrite,
			OutsideRoots: true,
		},
	}, gate.PostureBalanced)
	card, err := broker.buildWriteRootCard(
		command.SandboxWriteRootAsk{
			SessionID: "sess", ProjectID: projectID, ProjectDir: projectDir,
			ToolName: "command", Command: "docker build .",
		},
		"sess", "sess", projectDir, proposed,
		confine.WriteSubject{Kind: confine.WriteSubjectOrdinary},
		decision, nil,
	)
	testutil.FailErr(t, "buildWriteRootCard", err)
	action, plan := card.Action, card.Plan
	if action.Scope.ProjectID != projectID {
		t.Fatalf("action.Scope.ProjectID = %q, want %q", action.Scope.ProjectID, projectID)
	}
	var day *hitl.ApprovalOption
	for i := range plan.Options {
		opt := &plan.Options[i]
		if opt.Kind == hitl.ApprovalOptionLease && opt.Rung == hitl.ApprovalRungDay {
			day = opt
		}
		for _, delta := range opt.Authority {
			if delta.Grant == nil {
				continue
			}
			if delta.Grant.Scope != hitl.ApprovalGrantScopeProject {
				continue
			}
			if delta.Grant.ProjectID != projectID {
				t.Fatalf("%s grant %q project_id = %q, want %q", opt.Rung, delta.Grant.ID, delta.Grant.ProjectID, projectID)
			}
		}
	}
	if day == nil {
		t.Fatal("missing day lease")
	}
	var durable *hitl.ApprovalGrant
	for _, delta := range day.Authority {
		if delta.Kind == hitl.AuthorityGenericGrant && delta.Grant != nil {
			durable = delta.Grant
			break
		}
	}
	if durable == nil {
		t.Fatalf("day option has no durable grant: %+v", day.Authority)
	}
	// The Day rung bounds this chat's runtime root to the same day; the Chat
	// rung keeps it for the chat.
	for _, opt := range plan.Options {
		if opt.Kind != hitl.ApprovalOptionLease || opt.Group != "" {
			continue
		}
		for _, delta := range opt.Authority {
			if delta.Kind != hitl.AuthorityWriteRootChat {
				continue
			}
			want := 0
			if opt.Rung == hitl.ApprovalRungDay {
				want = hitl.DayRungTTLSeconds
			}
			if delta.TTLSeconds != want {
				t.Fatalf("%s write-root chat delta ttl = %d, want %d", opt.Rung, delta.TTLSeconds, want)
			}
		}
	}
	durable.GrantedByPersonID = testutil.HostOwner().ID
	if _, err := authority.ApplyGrant(*durable); err != nil {
		t.Fatalf("ApplyGrant day write_root: %v", err)
	}
}

func TestSessionOverlayCoveringKeepsMissingPath(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	root := "coord-sess"
	missing := filepath.Join(t.TempDir(), "not-yet-created")
	rt.GrantSessionWriteRoot(root, missing)
	got := sessionOverlayCovering(rt, root, filepath.Join(missing, "child"))
	if got == "" || !confine.PathWithinRoots(filepath.Join(missing, "child"), []string{got}) {
		t.Fatalf("covering = %q, want the live grant for %s", got, missing)
	}
	if sessionOverlayCovering(rt, root, filepath.Join(t.TempDir(), "other")) != "" {
		t.Fatal("unrelated path must not ride the live grant")
	}
}

func TestWriteRootAuthorizeSkipsCardWhenOverlayCovers(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPathGrantRuntime()
	missing := filepath.Join(t.TempDir(), "not-yet-created")
	rt.GrantSessionWriteRoot("sess", missing)
	broker := &WriteRootCheckpointBroker{
		Checkpoints: unusedWriteRootCheckpoints{t: t},
		Runtime:     rt,
	}
	got, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "sess", ProposedWriteRoot: missing,
	})
	testutil.FailErr(t, "Authorize covered overlay", err)
	if !got.Authorized || got.Raised {
		t.Fatalf("live write-root lease should authorize without a card: %+v", got)
	}
}

// A worker uses the write roots its chat approved without asking again.
func TestWorkerUsesItsChatsApprovedWriteRoot(t *testing.T) {
	t.Parallel()
	store := storetest.New(t, testdbseed.DefaultProjectID)
	parent, err := store.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create chat", err)
	child, err := store.Sessions.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create worker", err)
	rt := approvalstate.NewSandboxPathGrantRuntime()
	approved := filepath.Join(t.TempDir(), "approved")
	rt.GrantChat(parent.ID, approved, "grant-approved", "cp-1", nil)
	broker := &WriteRootCheckpointBroker{Checkpoints: unusedWriteRootCheckpoints{t: t}, Runtime: rt, Store: store.Sessions}
	got, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: child.ID, ParentSessionID: parent.ID, ProposedWriteRoot: approved,
	})
	testutil.FailErr(t, "Authorize worker write", err)
	if !got.Authorized || got.Raised {
		t.Fatalf("worker write under the chat's approval = %+v, want authorized without a card", got)
	}
}

// A key-material declaration asks; approval grants exactly the declared path.
func TestWriteRootAuthorizeAsksForKeyMaterial(t *testing.T) {
	floor := t.TempDir()
	confine.SetKeyMaterialPathsSource(func() []string { return []string{floor} })
	t.Cleanup(func() { confine.SetKeyMaterialPathsSource(nil) })

	checkpoints := &cannedWriteRootCheckpoints{approve: true}
	broker := &WriteRootCheckpointBroker{
		Checkpoints: checkpoints,
		Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
	}
	proposed := filepath.Join(floor, "authorized_keys")
	got, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "sess", ProposedWriteRoot: proposed, ToolName: "command",
	})
	testutil.FailErr(t, "Authorize key material", err)
	if checkpoints.requested == nil {
		t.Fatal("key material must mint a card, not fail silently")
	}
	if !got.Raised || !got.Authorized || got.ProposedWriteRoot != proposed {
		t.Fatalf("approved key-material ask = %+v", got)
	}
}

func TestWriteRootWorkerApprovalStaysWithChildSession(t *testing.T) {
	floor := t.TempDir()
	confine.SetKeyMaterialPathsSource(func() []string { return []string{floor} })
	t.Cleanup(func() { confine.SetKeyMaterialPathsSource(nil) })
	checkpoints := &cannedWriteRootCheckpoints{approve: true}
	broker := &WriteRootCheckpointBroker{Checkpoints: checkpoints, Runtime: approvalstate.NewSandboxPathGrantRuntime()}
	_, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "child", ParentSessionID: "parent", ProposedWriteRoot: filepath.Join(floor, "authorized_keys"), ToolName: "command",
	})
	testutil.FailErr(t, "Authorize worker key material", err)
	if checkpoints.requested == nil || checkpoints.requested.SessionID != "child" {
		t.Fatalf("checkpoint session = %+v, want child session", checkpoints.requested)
	}
}

// Credential stores remain gated inside attached roots.
func TestWriteRootAuthorizeAsksForCredentialStoreInsideProjectRoot(t *testing.T) {
	projectDir := t.TempDir()
	store := filepath.Join(projectDir, ".netrc")
	confine.SetCredentialStorePathsSource(func() []string { return []string{store} })
	t.Cleanup(func() { confine.SetCredentialStorePathsSource(nil) })

	checkpoints := &cannedWriteRootCheckpoints{approve: false}
	broker := &WriteRootCheckpointBroker{
		Checkpoints: checkpoints,
		Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
	}
	got, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "sess", ProjectDir: projectDir, ProposedWriteRoot: store, ToolName: "command",
	})
	testutil.FailErr(t, "Authorize in-root credential store", err)
	if checkpoints.requested == nil {
		t.Fatal("an in-root credential store must still mint a card")
	}
	if !got.Raised || got.Authorized || !got.Denied {
		t.Fatalf("rejected in-root credential ask = %+v", got)
	}
}

// Ancestor grants disclose and exclude protected paths.
func TestWriteRootAuthorizeAsksForStoreAncestorAndDisclosesProtected(t *testing.T) {
	// Keep the fixture outside ambient writable roots.
	base := "/opt/lycaon-broker-test-nonexistent"
	store := filepath.Join(base, ".docker", "config.json")
	confine.SetCredentialStorePathsSource(func() []string { return []string{store} })
	t.Cleanup(func() { confine.SetCredentialStorePathsSource(nil) })

	checkpoints := &cannedWriteRootCheckpoints{approve: true}
	rt := approvalstate.NewSandboxPathGrantRuntime()
	broker := &WriteRootCheckpointBroker{Checkpoints: checkpoints, Runtime: rt}

	proposed := filepath.Join(base, ".docker")
	got, authErr := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "sess", ProposedWriteRoot: proposed, ToolName: "command",
	})
	testutil.FailErr(t, "Authorize store ancestor", authErr)
	if checkpoints.requested == nil {
		t.Fatalf("a store ancestor must ask, not fail silently; got=%+v", got)
	}
	if !got.Raised || !got.Authorized || got.ProposedWriteRoot != proposed {
		t.Fatalf("approved store-ancestor ask = %+v", got)
	}

	plan := checkpoints.requested.ApprovalPlan
	if plan == nil || len(plan.Subject.Targets) == 0 {
		t.Fatalf("card carries no target: %+v", checkpoints.requested)
	}
	// JSON projection converts the string slice to []any.
	var disclosed []string
	if raw, ok := plan.Subject.Targets[0].Details["protected_paths_within"].([]any); ok {
		for _, entry := range raw {
			if s, isString := entry.(string); isString {
				disclosed = append(disclosed, s)
			}
		}
	}
	if len(disclosed) != 1 || disclosed[0] != fspath.CanonicalPath(store) {
		t.Fatalf("protected_paths_within = %v, want the catalogued store %q", disclosed, store)
	}

	// The installed lease passes the granted lane.
	if err := confine.ValidateGrantedWriteRoots(rt.SessionWriteRoots("sess")); err != nil {
		t.Fatalf("granted lease refused by the boundary: %v", err)
	}
}

// Control-plane writes are denied without a card or grant.
func TestWriteRootAuthorizeDeniesControlPlaneSilently(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	broker := &WriteRootCheckpointBroker{
		Checkpoints: unusedWriteRootCheckpoints{t: t},
		Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
	}
	got, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "sess", ProposedWriteRoot: filepath.Join(cfg, "credential-vault.age"),
	})
	testutil.FailErr(t, "Authorize control plane", err)
	if got.Authorized || got.Raised {
		t.Fatalf("control-plane write must deny without a card: %+v", got)
	}
}

// A key-material read declaration asks; approval grants exactly that file.
func TestReadPathAuthorizeAsksForKeyMaterial(t *testing.T) {
	floor := t.TempDir()
	confine.SetKeyMaterialPathsSource(func() []string { return []string{floor} })
	t.Cleanup(func() { confine.SetKeyMaterialPathsSource(nil) })

	checkpoints := &cannedWriteRootCheckpoints{approve: true}
	broker := &WriteRootCheckpointBroker{
		Checkpoints: checkpoints,
		Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
		ReadRuntime: approvalstate.NewSandboxPathGrantRuntime(),
	}
	proposed := filepath.Join(floor, "id_ed25519")
	got, err := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{
		SessionID: "sess", ProposedReadPath: proposed, ToolName: "command",
	})
	testutil.FailErr(t, "AuthorizeRead key material", err)
	if checkpoints.requested == nil {
		t.Fatal("a key-material read must mint a card")
	}
	if !got.Raised || !got.Authorized || got.ProposedReadPath != proposed {
		t.Fatalf("approved key-material read = %+v", got)
	}
}

// An undenied path needs no read grant; the declaration authorizes as a no-op.
func TestReadPathAuthorizeNoopForOrdinaryPaths(t *testing.T) {
	broker := &WriteRootCheckpointBroker{
		Checkpoints: unusedWriteRootCheckpoints{t: t},
		Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
		ReadRuntime: approvalstate.NewSandboxPathGrantRuntime(),
	}
	got, err := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{
		SessionID: "sess", ProposedReadPath: "/opt/lycaon-broker-test-nonexistent/notes.txt",
	})
	testutil.FailErr(t, "AuthorizeRead ordinary", err)
	if !got.Authorized || got.Raised {
		t.Fatalf("ordinary read declaration must authorize silently: %+v", got)
	}
}

// Control-plane reads deny without a card or grant.
func TestReadPathAuthorizeDeniesControlPlaneSilently(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	broker := &WriteRootCheckpointBroker{
		Checkpoints: unusedWriteRootCheckpoints{t: t},
		Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
		ReadRuntime: approvalstate.NewSandboxPathGrantRuntime(),
	}
	got, err := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{
		SessionID: "sess", ProposedReadPath: filepath.Join(cfg, "credential-vault.age"),
	})
	testutil.FailErr(t, "AuthorizeRead control plane", err)
	if got.Authorized || got.Raised {
		t.Fatalf("control-plane read must deny without a card: %+v", got)
	}
}

type cannedWriteRootCheckpoints struct {
	requested *hitl.CheckpointRequest
	approve   bool
}

func (c *cannedWriteRootCheckpoints) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	c.requested = &req
	return &hitl.CheckpointResponse{CheckpointID: "cp-1", Status: hitl.DecisionStatusPending}, nil
}

func (c *cannedWriteRootCheckpoints) PollCheckpoint(context.Context, string) (*hitl.CheckpointResponse, error) {
	if c.approve {
		return &hitl.CheckpointResponse{
			CheckpointID: "cp-1", Status: hitl.DecisionStatusApproved,
			Result: &hitl.DecisionResult{Approved: true},
		}, nil
	}
	return &hitl.CheckpointResponse{
		CheckpointID: "cp-1", Status: hitl.DecisionStatusRejected,
		Result: &hitl.DecisionResult{},
	}, nil
}

func (*cannedWriteRootCheckpoints) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}

func (*cannedWriteRootCheckpoints) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (*cannedWriteRootCheckpoints) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (*cannedWriteRootCheckpoints) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (*cannedWriteRootCheckpoints) PatchPendingToolApprovalAIRationale(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (*cannedWriteRootCheckpoints) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}
func (*cannedWriteRootCheckpoints) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

type unusedWriteRootCheckpoints struct{ t *testing.T }

func (u unusedWriteRootCheckpoints) RequestCheckpoint(context.Context, hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	u.t.Fatal("live write-root lease must not mint a card")
	return nil, nil
}
func (unusedWriteRootCheckpoints) PollCheckpoint(context.Context, string) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (unusedWriteRootCheckpoints) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (unusedWriteRootCheckpoints) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (unusedWriteRootCheckpoints) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (unusedWriteRootCheckpoints) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (unusedWriteRootCheckpoints) PatchPendingToolApprovalAIRationale(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (unusedWriteRootCheckpoints) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}
func (unusedWriteRootCheckpoints) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

func (*cannedWriteRootCheckpoints) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

func (unusedWriteRootCheckpoints) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

func TestReadPathApprovalCoversActionExclusions(t *testing.T) {
	for _, name := range []string{"cacert.pem", "credentials", "id_ed25519", "config.json"} {
		for _, approved := range []bool{false, true} {
			t.Run(name+"/approved="+fmt.Sprint(approved), func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, name)
				checkpoints := &cannedWriteRootCheckpoints{approve: approved}
				broker := &WriteRootCheckpointBroker{Checkpoints: checkpoints, ReadRuntime: approvalstate.NewSandboxPathGrantRuntime()}
				ask := command.SandboxReadPathAsk{SessionID: "sess", ToolName: "command", ProposedReadPath: path, ReadDenyPaths: []string{root}}
				got, err := broker.AuthorizeRead(t.Context(), ask)
				testutil.FailErr(t, "review excluded read", err)
				if checkpoints.requested == nil || !got.Raised || got.Authorized != approved || got.Denied == approved {
					t.Fatalf("read decision=%+v card=%+v", got, checkpoints.requested)
				}
				option := checkpoints.requested.ApprovalPlan.Options[0]
				if len(option.Authority) != 1 || option.Authority[0].Kind != hitl.AuthorityReadPathChat ||
					!slices.Equal(option.Authority[0].ReadPaths, []string{path}) {
					t.Fatalf("read approval authority=%+v", option.Authority)
				}
				if approved {
					// The API applies the selected option before completing a live checkpoint.
					delta := option.Authority[0]
					broker.ReadRuntime.GrantChat(delta.ChatSession(), delta.ReadPaths[0], delta.Grant.ID, "cp-1", nil)
				}
				paths := broker.SessionReadPaths(t.Context(), "sess", "")
				if approved && !slices.Equal(paths, []string{path}) || !approved && len(paths) != 0 {
					t.Fatalf("read grants=%v approved=%v", paths, approved)
				}
				if approved {
					checkpoints.requested = nil
					repeated, repeatErr := broker.AuthorizeRead(t.Context(), ask)
					testutil.FailErr(t, "reuse exact read grant", repeatErr)
					if !repeated.Authorized || checkpoints.requested != nil {
						t.Fatalf("exact grant was not reused: %+v", repeated)
					}
				}
			})
		}
	}
}

func TestReadPathApprovalUsesProtectedCatalogEntries(t *testing.T) {
	catalog, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "load sensitive path catalog", err)
	for _, name := range []string{"cacert.pem", "id_ed25519"} {
		t.Run(name, func(t *testing.T) {
			checkpoints := &cannedWriteRootCheckpoints{approve: true}
			broker := &WriteRootCheckpointBroker{Checkpoints: checkpoints, ReadRuntime: approvalstate.NewSandboxPathGrantRuntime(), Locations: catalog}
			path := filepath.Join(t.TempDir(), name)
			got, authorizeErr := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{SessionID: "sess", ProposedReadPath: path})
			testutil.FailErr(t, "review catalogued read", authorizeErr)
			if !got.Raised || !got.Authorized || checkpoints.requested == nil {
				t.Fatalf("catalogued read=%+v", got)
			}
		})
	}
}

func TestReadPathOwnScratchNeedsNoApprovalEvenUnderConfiguredReadDeny(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	scratch := filepath.Join(cfg, "scratch", "own")
	broker := &WriteRootCheckpointBroker{Checkpoints: unusedWriteRootCheckpoints{t: t}, ReadRuntime: approvalstate.NewSandboxPathGrantRuntime()}
	for _, path := range []string{filepath.Join(scratch, "file"), filepath.Join(cfg, "scratch", "other", "file"), filepath.Join(cfg, "store.db")} {
		got, err := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{SessionID: "own", ProposedReadPath: path, SessionScratchRoot: scratch, ReadDenyPaths: []string{cfg}})
		testutil.FailErr(t, "authorize session scratch", err)
		if got.Authorized != (path == filepath.Join(scratch, "file")) || got.Raised {
			t.Fatalf("path=%s result=%+v", path, got)
		}
	}
}

func (*cannedWriteRootCheckpoints) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}

func (unusedWriteRootCheckpoints) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestWriteRootBrokerRefusesInvalidScratchContainingState(t *testing.T) {
	ancestor := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(ancestor, "state"))
	broker := &WriteRootCheckpointBroker{Checkpoints: unusedWriteRootCheckpoints{t: t}, Runtime: approvalstate.NewSandboxPathGrantRuntime()}
	result, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{SessionID: "session", ProposedWriteRoot: ancestor, SessionScratchRoot: ancestor})
	testutil.FailErr(t, "refuse scratch ancestor without asking", err)
	if result.Authorized || result.Raised {
		t.Fatalf("invalid scratch acquired authority: %+v", result)
	}
}

func TestWriteRootBrokerRejectsInvalidAttachedAuthority(t *testing.T) {
	ancestor := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(ancestor, "state"))
	broker := &WriteRootCheckpointBroker{Checkpoints: unusedWriteRootCheckpoints{t: t}, Runtime: approvalstate.NewSandboxPathGrantRuntime()}
	result, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{SessionID: "session", ProjectDir: ancestor, ProposedWriteRoot: t.TempDir()})
	var refusal *confine.WriteRootRefusalError
	if !errors.As(err, &refusal) || refusal.Code != confine.WriteRootCodeContainsState || result.Authorized || result.Raised {
		t.Fatalf("invalid attached authority reached approval: result=%+v error=%v", result, err)
	}
}

// The broker grants only what the boundary's granted lane admits: a proposed
// root that holds host state is refused without a card and leaves no grant.
func TestWriteRootBrokerRefusesGrantedLaneRootWithoutAsking(t *testing.T) {
	ancestor := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(ancestor, "state"))
	if refused, code := confine.GrantedWriteRootRefused(ancestor); !refused || code != confine.WriteRootCodeContainsState {
		t.Fatalf("fixture root granted-lane verdict = %v %q, want %q", refused, code, confine.WriteRootCodeContainsState)
	}
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	broker := &WriteRootCheckpointBroker{Checkpoints: unusedWriteRootCheckpoints{t: t}, Runtime: runtime}
	result, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{SessionID: "session", ProposedWriteRoot: ancestor})
	testutil.FailErr(t, "refuse granted-lane root", err)
	if result.Authorized || result.Raised {
		t.Fatalf("state-holding root acquired authority: %+v", result)
	}
	if roots := runtime.SessionWriteRoots("session"); len(roots) != 0 {
		t.Fatalf("refusal left session write roots %v", roots)
	}
}

type tempDecisionFixture struct {
	unusedWriteRootCheckpoints
	status       hitl.DecisionStatus
	runtime      *approvalstate.SandboxPathGrantRuntime
	readRuntime  *approvalstate.SandboxPathGrantRuntime
	grantedPaths *grantedpath.Runtime
	cards        []hitl.CheckpointRequest
}

func (c *tempDecisionFixture) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	c.cards = append(c.cards, req)
	if c.status == hitl.DecisionStatusApproved {
		for _, opt := range req.ApprovalPlan.Options {
			if opt.Rung != hitl.ApprovalRungChat || opt.Disabled || opt.Group != "" {
				continue
			}
			for _, delta := range opt.Authority {
				switch delta.Kind {
				case hitl.AuthorityWriteRootChat:
					if c.runtime != nil {
						for _, path := range delta.WriteRoots {
							c.runtime.GrantChat(delta.ChatSession(), path, delta.Grant.ID, "checkpoint", nil)
						}
					}
				case hitl.AuthorityReadPathChat:
					if c.readRuntime != nil {
						for _, path := range delta.ReadPaths {
							c.readRuntime.GrantChat(delta.ChatSession(), path, delta.Grant.ID, "checkpoint", nil)
						}
					}
				case hitl.AuthorityGrantedPath:
					if c.grantedPaths != nil && delta.GrantedPath != nil {
						mode := grantedpath.ModeRead
						if delta.GrantedPath.Write && delta.GrantedPath.WriteOnly {
							mode = grantedpath.ModeWriteOnly
						} else if delta.GrantedPath.Write {
							mode = grantedpath.ModeWrite
						}
						_, _ = c.grantedPaths.Grant(delta.ChatSession(), grantedpath.Grant{
							ID: delta.Grant.ID, SourceCheckpointID: "checkpoint",
							Path: delta.GrantedPath.Path, Mode: mode, Tree: delta.GrantedPath.Tree,
						})
					}
				default:
				}
			}
			break
		}
	}
	return &hitl.CheckpointResponse{CheckpointID: "checkpoint", Status: c.status, ResolvedBy: authzledger.ResolvedByHuman, Result: &hitl.DecisionResult{Approved: c.status == hitl.DecisionStatusApproved}}, nil
}

func (c *tempDecisionFixture) GetCheckpoint(_ context.Context, id string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: id, Status: c.status, ResolvedBy: authzledger.ResolvedByHuman, Result: &hitl.DecisionResult{Approved: c.status == hitl.DecisionStatusApproved}}, nil
}

func (c *tempDecisionFixture) PollCheckpoint(_ context.Context, id string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: id, Status: c.status, ResolvedBy: authzledger.ResolvedByHuman, Result: &hitl.DecisionResult{Approved: c.status == hitl.DecisionStatusApproved}}, nil
}

func TestSharedTempSettlementAndChatScope(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", "/opt/shared-temp-review-fixture/config")
	roots := []string{confine.SharedTempLocation("/tmp"), confine.SharedTempLocation("/var/tmp")}
	obs := confine.SharedTempObservation{Writes: roots}
	for _, status := range []hitl.DecisionStatus{hitl.DecisionStatusApproved, hitl.DecisionStatusRejected, hitl.DecisionStatusExpired, hitl.DecisionStatusCanceled} {
		t.Run(string(status), func(t *testing.T) {
			runtime := approvalstate.NewSandboxPathGrantRuntime()
			cards := &tempDecisionFixture{runtime: runtime, status: status}
			broker := &WriteRootCheckpointBroker{Runtime: runtime, Checkpoints: cards, Posture: func(string) gate.Posture { return gate.PostureBalanced }}
			in := command.SandboxWriteRootAsk{SessionID: "chat", ToolName: "command", ToolCallID: "first", Command: "build"}
			result := broker.ReviewSharedTemp(t.Context(), in, obs, true)
			if len(cards.cards) != 1 || len(cards.cards[0].ApprovalPlan.Subject.Targets) != len(roots) {
				t.Fatalf("reviewed locations=%v result=%+v", cards.cards, result)
			}
			for i, target := range cards.cards[0].ApprovalPlan.Subject.Targets {
				wantLabel := "Create, change, and remove files in " + roots[i]
				if target.Kind != "shared_temp" || target.Label != wantLabel {
					t.Fatalf("target %d=%+v, want label %q", i, target, wantLabel)
				}
			}
			outcome := map[hitl.DecisionStatus][]string{
				hitl.DecisionStatusApproved: result.Allowed,
				hitl.DecisionStatusRejected: result.Declined,
				hitl.DecisionStatusExpired:  result.Unanswered,
				hitl.DecisionStatusCanceled: result.Unanswered,
			}[status]
			if !slices.Equal(outcome, roots) || len(result.Allowed)+len(result.Declined)+len(result.Unanswered) != len(roots) {
				t.Fatalf("status=%s outcome=%+v", status, result)
			}
			in.ToolCallID = "second"
			broker.ReviewSharedTemp(t.Context(), in, obs, true)
			expected := 1
			if status == hitl.DecisionStatusExpired || status == hitl.DecisionStatusCanceled {
				expected = 2
			}
			if len(cards.cards) != expected {
				t.Fatalf("second call raised %d cards", len(cards.cards)-1)
			}
			if other := broker.Runtime.SessionWriteRoots("other"); len(other) > 0 {
				t.Fatalf("foreign grant %v", other)
			}
		})
	}
}

func TestSharedTempToleratedOnceAndLightAuthorization(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", "/opt/shared-temp-review-fixture/config")
	roots := []string{confine.SharedTempLocation("/tmp"), confine.SharedTempLocation("/var/tmp")}
	obs := confine.SharedTempObservation{Writes: roots}
	rt := approvalstate.NewSandboxPathGrantRuntime()
	cards := &tempDecisionFixture{runtime: rt, status: hitl.DecisionStatusApproved}
	broker := &WriteRootCheckpointBroker{Runtime: rt, Checkpoints: cards, Posture: func(string) gate.Posture { return gate.PostureLight }, ApprovalsDisabled: func(string) bool { return true }}
	ask := command.SandboxWriteRootAsk{SessionID: "chat", ToolName: "verify"}
	for i := 0; i < 3; i++ {
		result := broker.ReviewSharedTemp(t.Context(), ask, obs, false)
		if (len(result.Refused) > 0) != (i == 0) || result.Tolerated != (i == 0) {
			t.Fatalf("run %d: %+v", i, result)
		}
	}
	ask.SessionID = "other"
	other := broker.ReviewSharedTemp(t.Context(), ask, obs, false)
	if !slices.Equal(other.Refused, roots) {
		t.Fatalf("other chat report=%+v", other)
	}
	result := broker.ReviewSharedTemp(t.Context(), ask, obs, true)
	rt.ReleaseRun(ask.SessionID)
	if !slices.Equal(result.Allowed, roots) || !slices.Equal(rt.SessionWriteRoots(ask.SessionID), roots) || len(cards.cards) != 1 {
		t.Fatalf("light review=%+v cards=%d", result, len(cards.cards))
	}
}

func TestSharedTempExchangeSettlementCombinesReadsAndWritesAndReopensOnExplicitPreflight(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", "/opt/shared-temp-review-fixture/config")
	tempSub, err := os.MkdirTemp("/tmp", "pwc-exchange-settle-") //nolint:usetesting // Must reside in shared temp.
	testutil.FailErr(t, "create shared temp fixture", err)
	t.Cleanup(func() { _ = os.RemoveAll(tempSub) })
	tempSub = grantedpath.Normalize(tempSub)
	inputFile := filepath.Join(tempSub, "incoming.csv")
	testutil.FailErr(t, "write input file", os.WriteFile(inputFile, []byte("id,val\n1,2\n"), 0o600))
	exportFile := filepath.Join(tempSub, "export", "result.csv")
	exportDir := filepath.Dir(exportFile)
	speculativeMissing := filepath.Join(tempSub, "absent-probe.conf")

	refusals := confine.SandboxRefusals{
		Refusals: []confine.SandboxRefusal{
			{Operation: "file-read-data", Target: inputFile, Layer: confine.FloorSharedTemp, Grant: inputFile},
			{Operation: "file-read-data", Target: speculativeMissing, Layer: confine.FloorSharedTemp, Grant: speculativeMissing},
			{Operation: "file-write-create", Target: exportFile, Layer: confine.FloorSharedTemp, Grant: exportDir},
		},
	}
	obs := refusals.SharedTempObservation()
	if !slices.Equal(obs.Reads, []string{inputFile}) || !slices.Equal(obs.Writes, []string{exportDir}) {
		t.Fatalf("SharedTempObservation = %+v, want reads=[%s] writes=[%s]", obs, inputFile, exportDir)
	}

	writeRT := approvalstate.NewSandboxPathGrantRuntime()
	readRT := approvalstate.NewSandboxPathGrantRuntime()
	gpRT := grantedpath.NewRuntime()
	cards := &tempDecisionFixture{
		runtime: writeRT, readRuntime: readRT, grantedPaths: gpRT,
		status: hitl.DecisionStatusRejected,
	}
	broker := &WriteRootCheckpointBroker{
		Runtime: writeRT, ReadRuntime: readRT, GrantedPaths: gpRT,
		Checkpoints: cards, Posture: func(string) gate.Posture { return gate.PostureLight },
	}
	ask := command.SandboxWriteRootAsk{SessionID: "chat-ex", ToolName: "command", ToolCallID: "c1", Command: "transform"}
	declined := broker.ReviewSharedTemp(t.Context(), ask, obs, true)
	if len(cards.cards) != 1 {
		t.Fatalf("cards = %d, want 1 combined exchange card", len(cards.cards))
	}
	plan := cards.cards[0].ApprovalPlan
	if plan.Subject.Title != "Allow this chat to exchange files in system temp?" || len(plan.Subject.Targets) != 2 {
		t.Fatalf("unexpected combined plan subject: %+v", plan.Subject)
	}
	if len(declined.Declined) != 2 {
		t.Fatalf("declined settlement = %+v", declined)
	}
	ask.ToolCallID = "c2"
	repeat := broker.ReviewSharedTemp(t.Context(), ask, obs, true)
	if len(cards.cards) != 1 || len(repeat.Declined) != 2 {
		t.Fatalf("declined scope raised duplicate automatic card: cards=%d repeat=%+v", len(cards.cards), repeat)
	}

	cards.status = hitl.DecisionStatusApproved
	readRes, err := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{
		SessionID: "chat-ex", ToolName: "command", ToolCallID: "c3", Command: "transform", ProposedReadPath: inputFile,
	})
	testutil.FailErr(t, "explicit preflight read after decline", err)
	writeRes, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "chat-ex", ToolName: "command", ToolCallID: "c3", Command: "transform", ProposedWriteRoot: exportDir,
	})
	testutil.FailErr(t, "explicit preflight write after decline", err)
	if !readRes.Authorized || !writeRes.Authorized || len(cards.cards) != 3 {
		t.Fatalf("explicit preflight did not reopen review: read=%+v write=%+v cards=%d", readRes, writeRes, len(cards.cards))
	}
	_, canReadInput := gpRT.Covers("chat-ex", "", inputFile, grantedpath.ModeRead)
	_, canWriteInput := gpRT.Covers("chat-ex", "", inputFile, grantedpath.ModeWrite)
	if !canReadInput || canWriteInput {
		t.Fatal("input file grant was not read-only")
	}
	_, canWriteExport := gpRT.Covers("chat-ex", "", exportFile, grantedpath.ModeWrite)
	_, canReadExport := gpRT.Covers("chat-ex", "", exportFile, grantedpath.ModeRead)
	if !canWriteExport || canReadExport {
		t.Fatal("export directory grant did not stay write-only")
	}
}

func TestSharedTempRelatedRootsNeedReviewWithApprovalsDisabled(t *testing.T) {
	location := t.TempDir()
	t.Setenv("TMPDIR", location)
	broker := &WriteRootCheckpointBroker{
		Runtime: approvalstate.NewSandboxPathGrantRuntime(), Checkpoints: &tempDecisionFixture{},
		ApprovalsDisabled: func(string) bool { return true },
	}
	for _, path := range []string{location, filepath.Join(location, "build"), filepath.Dir(location)} {
		built := false
		result, err := broker.authorizeWriteRoot(t.Context(), command.SandboxWriteRootAsk{SessionID: "chat", ProposedWriteRoot: path}, func(context.Context, sandboxAskRequest) (sandboxAskResolution, error) {
			built = true
			return sandboxAskResolution{Answered: true, Denied: true}, nil
		})
		if err != nil || !built || result.Authorized || !result.Denied {
			t.Fatalf("root %s result=%+v built=%t error=%v", path, result, built, err)
		}
	}
}
