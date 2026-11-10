package command_test

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
	"time"
)

type oneShotFileAccessPolicy struct {
	*toolprofiles.ProfilePolicyEngine
	approved func() []hitl.GrantedPathDelta
}

func (p oneShotFileAccessPolicy) Evaluate(ctx context.Context, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	decision, err := p.ProfilePolicyEngine.Evaluate(ctx, eval)
	if err != nil || decision == nil || decision.Blocked {
		return decision, err
	}
	if p.approved != nil {
		if deltas := p.approved(); len(deltas) > 0 {
			return &platform.PolicyDecision{
				Allowed:            false,
				RequiresApproval:   true,
				ApprovalActionType: eval.ToolName,
				Approval: &hitl.ApprovalResult{
					FileAccess: append([]hitl.GrantedPathDelta(nil), deltas...),
				},
			}, nil
		}
	}
	return decision, nil
}

type autoApproveCheckpoints struct{}

func (autoApproveCheckpoints) RequestCheckpoint(_ context.Context, _ hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: "chk-oneshot", Status: hitl.DecisionStatusPending}, nil
}

func (autoApproveCheckpoints) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{
		CheckpointID: checkpointID,
		Status:       hitl.DecisionStatusApproved,
		Result:       &hitl.DecisionResult{Approved: true},
	}, nil
}

func (autoApproveCheckpoints) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (autoApproveCheckpoints) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (autoApproveCheckpoints) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (autoApproveCheckpoints) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (autoApproveCheckpoints) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}
func (autoApproveCheckpoints) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (autoApproveCheckpoints) PatchPendingToolApprovalAIRationale(context.Context, string, string) error {
	return nil
}
func (autoApproveCheckpoints) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}
func (autoApproveCheckpoints) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

func TestExternalSpawnUsesOnlyAdmittedWriteRoots(t *testing.T) {
	requireAppliedBoundary(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	testutil.FailErr(t, "find home", err)
	outside, err := os.MkdirTemp(account.HomeDir, ".paintedwolf-grant-") //nolint:usetesting // Must lie outside default temporary write roots.
	testutil.FailErr(t, "create outside directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	approved := filepath.Join(outside, "approved")
	testutil.FailErr(t, "create approved directory", os.Mkdir(approved, 0700))
	f := newProgramFixture(t)
	granted := true
	f.Runtime.Executor.Boundary.SetSessionWriteRootOverlay(func(context.Context, string, string) []string {
		if granted {
			return []string{approved}
		}
		return nil
	})
	for _, tc := range []struct {
		path    string
		allowed bool
	}{
		{filepath.Join(approved, "result"), true},
		{filepath.Join(outside, "unapproved"), false},
	} {
		out, err := f.call(t, t.Context(), "external -- /bin/sh -c 'printf ok > \"$1\"' sh "+strconv.Quote(tc.path))
		testutil.FailErr(t, "execute write", err)
		if got := decodeOutcome(t, out); (got.Status == 0) != tc.allowed {
			t.Fatalf("write %s allowed=%v: %+v", tc.path, tc.allowed, got)
		}
		data, err := os.ReadFile(tc.path)
		if tc.allowed && (err != nil || string(data) != "ok") || !tc.allowed && !os.IsNotExist(err) {
			t.Fatalf("write effect %s: %q, %v", tc.path, data, err)
		}
	}
	granted = false
	out, err := f.call(t, t.Context(), "external -- /bin/sh -c 'printf changed > \"$1\"' sh "+strconv.Quote(filepath.Join(approved, "result")))
	testutil.FailErr(t, "execute without grant", err)
	if decodeOutcome(t, out).Status == 0 {
		t.Fatal("grant escaped its admitted invocation")
	}
	data, err := os.ReadFile(filepath.Join(approved, "result"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("ungranted invocation changed file: %q, %v", data, err)
	}
}

func TestExternalSpawnAttachedControlPlaneRootFallsBackToScratchAndDeniesState(t *testing.T) {
	requireAppliedBoundary(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")

	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	testutil.FailErr(t, "find home", err)
	rawHome, err := os.MkdirTemp(account.HomeDir, ".paintedwolf-pr03-ctrl-") //nolint:usetesting // Must lie outside default temporary write roots.
	testutil.FailErr(t, "create home directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(rawHome) })
	home := fspath.CanonicalPath(rawHome)

	configDir := filepath.Join(home, ".paintedwolf")
	testutil.FailErr(t, "mkdir config", os.MkdirAll(configDir, 0o700))
	configDir = fspath.CanonicalPath(configDir)
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	apiToken := filepath.Join(configDir, "api.token")
	settingsYAML := filepath.Join(configDir, "settings.yaml")
	storeDB := filepath.Join(configDir, "store.db")
	ownScratch := filepath.Join(configDir, "scratch", "chat-own")
	otherScratch := filepath.Join(configDir, "scratch", "chat-other")
	otherFlow := filepath.Join(otherScratch, "flow.sh")
	testutil.FailErr(t, "mkdir own scratch", os.MkdirAll(ownScratch, 0o700))
	testutil.FailErr(t, "mkdir other scratch", os.MkdirAll(otherScratch, 0o700))
	testutil.FailErr(t, "write api.token", os.WriteFile(apiToken, []byte("secret-token"), 0o600))
	testutil.FailErr(t, "write settings.yaml", os.WriteFile(settingsYAML, []byte("mode: strict\n"), 0o600))
	testutil.FailErr(t, "write store.db", os.WriteFile(storeDB, []byte("sqlite-bytes"), 0o600))
	testutil.FailErr(t, "write other flow.sh", os.WriteFile(otherFlow, []byte("echo other\n"), 0o600))

	f := newProgramFixture(t)
	f.Dir = configDir
	f.ScratchDir = ownScratch

	probeTC := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "proj-ctrl", SessionID: f.Session},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: configDir, IsPrimary: true}}, ActiveRootID: "r1"},
		Host:     tools.InvocationHost{SessionScratchDir: ownScratch, SessionOutputDir: f.OutputDir},
	}
	cwd, display, err := projectpaths.CommandCwd(t.Context(), probeTC, "")
	testutil.FailErr(t, "CommandCwd fallback", err)
	if cwd != ownScratch || display != "@scratch" {
		t.Fatalf("CommandCwd on attached configdir.Root() = (%q, %q), want (%q, \"@scratch\")", cwd, display, ownScratch)
	}
	spawnReq, reject := tools.ConfineRequestForSpawn(t.Context(), probeTC, nil)
	if reject != nil {
		t.Fatalf("ConfineRequestForSpawn rejected attached configDir: %+v", reject)
	}
	c, ok := confine.DefaultConfinement(spawnReq)
	if !ok {
		t.Fatal("DefaultConfinement disabled for attached configDir")
	}
	writeRoots := confine.BoundaryOf(c).WriteRoots
	if slices.Contains(writeRoots, configDir) || !slices.Contains(writeRoots, ownScratch) {
		t.Fatalf("BoundaryOf(c).WriteRoots = %v; want %q excluded and %q included", writeRoots, configDir, ownScratch)
	}

	out, err := f.call(t, t.Context(), "external -- /bin/sh -c 'pwd && printf scratch-ok > ./probe.txt && cat ./probe.txt'")
	testutil.FailErr(t, "execute scratch fallback command", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || !strings.Contains(got.Stdout, ownScratch) || !strings.Contains(got.Stdout, "scratch-ok") {
		t.Fatalf("scratch fallback command outcome = %+v, want status 0 with cwd %q and scratch-ok", got, ownScratch)
	}

	for _, denied := range []string{apiToken, settingsYAML, storeDB, otherFlow} {
		out, err = f.call(t, t.Context(), "external -- /bin/cat "+strconv.Quote(denied))
		testutil.FailErr(t, "execute denied read", err)
		if decodeOutcome(t, out).Status == 0 {
			t.Fatalf("confined command read denied control-plane path %s", denied)
		}
		out, err = f.call(t, t.Context(), "external -- /bin/sh -c 'printf pwned > \"$1\"' sh "+strconv.Quote(denied))
		testutil.FailErr(t, "execute denied write", err)
		if decodeOutcome(t, out).Status == 0 {
			t.Fatalf("confined command wrote denied control-plane path %s", denied)
		}
	}

	renamedCfg := filepath.Join(home, ".paintedwolf-Moved")
	out, err = f.call(t, t.Context(), "external -- /bin/sh -c 'rmdir \"$1\" || mv \"$1\" \"$2\"' sh "+strconv.Quote(configDir)+" "+strconv.Quote(renamedCfg))
	testutil.FailErr(t, "execute rmdir/mv on configDir", err)
	if decodeOutcome(t, out).Status == 0 {
		t.Fatalf("confined command removed or renamed configDir %s", configDir)
	}
	if _, statErr := os.Stat(configDir); statErr != nil {
		t.Fatalf("configDir missing after rmdir/mv attempt: %v", statErr)
	}
}

func TestExternalSpawnAttachedProtectedRootGrantAndRevokeLifecycle(t *testing.T) {
	requireAppliedBoundary(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")

	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	testutil.FailErr(t, "find home", err)
	rawHome, err := os.MkdirTemp(account.HomeDir, ".paintedwolf-pr03-ssh-") //nolint:usetesting // Must lie outside default temporary write roots.
	testutil.FailErr(t, "create home directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(rawHome) })
	home := fspath.CanonicalPath(rawHome)

	configDir := filepath.Join(home, ".paintedwolf")
	sshDir := filepath.Join(home, ".ssh")
	ordinaryDir := filepath.Join(home, "workspace")
	testutil.FailErr(t, "mkdir config", os.MkdirAll(configDir, 0o700))
	testutil.FailErr(t, "mkdir .ssh", os.MkdirAll(sshDir, 0o700))
	testutil.FailErr(t, "mkdir workspace", os.MkdirAll(ordinaryDir, 0o700))
	configDir = fspath.CanonicalPath(configDir)
	sshDir = fspath.CanonicalPath(sshDir)
	ordinaryDir = fspath.CanonicalPath(ordinaryDir)
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	previousKeys := confine.KeyMaterialHomeRelPaths()
	confine.SetKeyMaterialPathsSource(func() []string { return []string{"~/.ssh/"} })
	t.Cleanup(func() {
		if len(previousKeys) == 0 {
			confine.SetKeyMaterialPathsSource(nil)
			return
		}
		confine.SetKeyMaterialPathsSource(func() []string { return previousKeys })
	})

	ownScratch := filepath.Join(configDir, "scratch", "chat-own")
	testutil.FailErr(t, "mkdir own scratch", os.MkdirAll(ownScratch, 0o700))
	ordinaryFile := filepath.Join(ordinaryDir, "readme.txt")
	privKey := filepath.Join(sshDir, "id_ed25519")
	pubKey := filepath.Join(sshDir, "id_ed25519.pub")
	otherKey := filepath.Join(sshDir, "id_rsa")
	authKeys := filepath.Join(sshDir, "authorized_keys")
	authKeysTest := filepath.Join(sshDir, "authorized_keys_test")
	testutil.FailErr(t, "write ordinary readme", os.WriteFile(ordinaryFile, []byte("PUBLIC-README"), 0o644))
	testutil.FailErr(t, "write id_ed25519", os.WriteFile(privKey, []byte("PRIVATE-ED25519"), 0o600))
	testutil.FailErr(t, "write id_ed25519.pub", os.WriteFile(pubKey, []byte("ssh-ed25519 AAAA-PUBLIC"), 0o644))
	testutil.FailErr(t, "write id_rsa", os.WriteFile(otherKey, []byte("PRIVATE-RSA"), 0o600))
	testutil.FailErr(t, "write authorized_keys", os.WriteFile(authKeys, []byte("ssh-ed25519 INITIAL\n"), 0o600))
	testutil.FailErr(t, "write authorized_keys_test", os.WriteFile(authKeysTest, []byte("ssh-ed25519 INITIAL-TEST\n"), 0o600))

	f := newProgramFixture(t)
	f.Dir = sshDir
	f.ScratchDir = ownScratch
	f.Configure = func(tc *tools.ToolContext) {
		tc.Source.Roots = []projectroot.RootRef{
			{ID: "r1", Label: "ssh", Path: sshDir, IsPrimary: true},
			{ID: "r2", Label: "workspace", Path: ordinaryDir},
		}
	}

	probeTC := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "proj-ssh", SessionID: f.Session},
		Source: tools.InvocationSource{
			Roots: []projectroot.RootRef{
				{ID: "r1", Label: "ssh", Path: sshDir, IsPrimary: true},
				{ID: "r2", Label: "workspace", Path: ordinaryDir},
			},
			ActiveRootID: "r1",
		},
		Host: tools.InvocationHost{SessionScratchDir: ownScratch, SessionOutputDir: f.OutputDir},
		Files: tools.InvocationFiles{
			ApprovedFileAccess: []hitl.GrantedPathDelta{
				{Path: privKey, Write: false},
				{Path: authKeys, Write: true},
			},
		},
	}
	cwd, display, err := projectpaths.CommandCwd(t.Context(), probeTC, "")
	testutil.FailErr(t, "CommandCwd fallback for attached .ssh", err)
	if cwd != ownScratch || display != "@scratch" {
		t.Fatalf("CommandCwd on attached ~/.ssh = (%q, %q), want (%q, \"@scratch\")", cwd, display, ownScratch)
	}
	spawnReq, reject := tools.ConfineRequestForSpawn(t.Context(), probeTC, nil)
	if reject != nil {
		t.Fatalf("ConfineRequestForSpawn rejected attached .ssh: %+v", reject)
	}
	if len(spawnReq.ProtectedReadGrants) != 1 || spawnReq.ProtectedReadGrants[0].ResolvedPath != privKey {
		t.Fatalf("ApprovedFileAccess read conversion = %+v, want exact grant for %q", spawnReq.ProtectedReadGrants, privKey)
	}
	if len(spawnReq.ProtectedWriteGrants) != 1 || spawnReq.ProtectedWriteGrants[0].ResolvedPath != authKeys {
		t.Fatalf("ApprovedFileAccess write conversion = %+v, want exact grant for %q", spawnReq.ProtectedWriteGrants, authKeys)
	}
	c, ok := confine.DefaultConfinement(spawnReq)
	if !ok {
		t.Fatal("DefaultConfinement disabled for attached .ssh")
	}
	writeRoots := confine.BoundaryOf(c).WriteRoots
	if slices.Contains(writeRoots, sshDir) || !slices.Contains(writeRoots, ownScratch) || !slices.Contains(writeRoots, ordinaryDir) {
		t.Fatalf("BoundaryOf(c).WriteRoots = %v; want %q excluded and %q, %q included", writeRoots, sshDir, ownScratch, ordinaryDir)
	}

	var oneShotApproved []hitl.GrantedPathDelta
	f.Runtime.Executor = toolexecution.NewExecutor(oneShotFileAccessPolicy{
		ProfilePolicyEngine: toolprofiles.NewProfilePolicyEngine(f.Boundary),
		approved:            func() []hitl.GrantedPathDelta { return append([]hitl.GrantedPathDelta(nil), oneShotApproved...) },
	}, f.Runtime.Registry, "fixture")
	f.Runtime.Executor.Approvals.SetCheckpointManager(t.Context(), autoApproveCheckpoints{}, nil)
	callWithApprovedAccess := func(approved []hitl.GrantedPathDelta, command string) (string, error) {
		oneShotApproved = append([]hitl.GrantedPathDelta(nil), approved...)
		defer func() { oneShotApproved = nil }()
		return f.call(t, t.Context(), command)
	}

	var readOverlay, writeOverlay []string
	f.Runtime.Executor.Boundary.SetSessionReadPathOverlay(func(context.Context, string, string) []string {
		return append([]string(nil), readOverlay...)
	})
	f.Runtime.Executor.Boundary.SetSessionWriteRootOverlay(func(context.Context, string, string) []string {
		return append([]string(nil), writeOverlay...)
	})

	// Phase 1: Unapproved — ordinary readable file outside .ssh and own scratch succeed; .ssh reads (including .pub) and writes fail.
	out, err := f.call(t, t.Context(), "external -- /bin/sh -c 'pwd && cat \"$1\" && printf ok > ./scratch-ok.txt && cat ./scratch-ok.txt' sh "+strconv.Quote(ordinaryFile))
	testutil.FailErr(t, "phase 1 ordinary read and scratch write", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || !strings.Contains(got.Stdout, ownScratch) || !strings.Contains(got.Stdout, "PUBLIC-README") {
		t.Fatalf("phase 1 ordinary read/scratch outcome = %+v", got)
	}
	for _, deniedRead := range []string{privKey, pubKey, otherKey, authKeys} {
		out, err = f.call(t, t.Context(), "external -- /bin/cat "+strconv.Quote(deniedRead))
		testutil.FailErr(t, "phase 1 denied read", err)
		if decodeOutcome(t, out).Status == 0 {
			t.Fatalf("phase 1 unapproved read of %s succeeded", deniedRead)
		}
	}
	for _, deniedWrite := range []string{authKeys, authKeysTest, privKey, filepath.Join(sshDir, "evil.txt")} {
		out, err = f.call(t, t.Context(), "external -- /bin/sh -c 'printf pwned > \"$1\"' sh "+strconv.Quote(deniedWrite))
		testutil.FailErr(t, "phase 1 denied write", err)
		if decodeOutcome(t, out).Status == 0 {
			t.Fatalf("phase 1 unapproved write of %s succeeded", deniedWrite)
		}
	}

	// Phase 2a: Live one-shot ApprovedFileAccess read grant (Write: false) on id_ed25519 with no session overlay.
	oneShotReadGrant := []hitl.GrantedPathDelta{{Path: privKey, Write: false}}
	out, err = callWithApprovedAccess(oneShotReadGrant, "external -- /bin/cat "+strconv.Quote(privKey))
	testutil.FailErr(t, "phase 2a one-shot approved read", err)
	if got = decodeOutcome(t, out); got.Status != 0 || !strings.Contains(got.Stdout, "PRIVATE-ED25519") {
		t.Fatalf("phase 2a one-shot approved read of id_ed25519 failed: %+v", got)
	}
	for _, deniedRead := range []string{pubKey, otherKey, authKeys} {
		out, err = callWithApprovedAccess(oneShotReadGrant, "external -- /bin/cat "+strconv.Quote(deniedRead))
		testutil.FailErr(t, "phase 2a one-shot sibling read", err)
		if decodeOutcome(t, out).Status == 0 {
			t.Fatalf("phase 2a one-shot read grant leaked to sibling %s", deniedRead)
		}
	}
	out, err = callWithApprovedAccess(oneShotReadGrant, "external -- /bin/sh -c 'printf pwned > \"$1\"' sh "+strconv.Quote(authKeys))
	testutil.FailErr(t, "phase 2a one-shot read grant write attempt", err)
	if decodeOutcome(t, out).Status == 0 {
		t.Fatal("phase 2a one-shot read grant allowed writing authorized_keys")
	}

	// Phase 2b: Live one-shot ApprovedFileAccess write grant (Write: true) on authorized_keys with no session overlay.
	oneShotWriteGrant := []hitl.GrantedPathDelta{{Path: authKeys, Write: true}}
	out, err = callWithApprovedAccess(oneShotWriteGrant, "external -- /bin/sh -c 'printf one-shot-key > \"$1\"' sh "+strconv.Quote(authKeys))
	testutil.FailErr(t, "phase 2b one-shot approved write", err)
	if got = decodeOutcome(t, out); got.Status != 0 {
		t.Fatalf("phase 2b one-shot approved write to authorized_keys failed: %+v", got)
	}
	if data, err := os.ReadFile(authKeys); err != nil || string(data) != "one-shot-key" {
		t.Fatalf("phase 2b authorized_keys after one-shot write = %q, %v; want \"one-shot-key\"", data, err)
	}
	for _, deniedWrite := range []string{privKey, authKeysTest, filepath.Join(sshDir, "evil.txt")} {
		out, err = callWithApprovedAccess(oneShotWriteGrant, "external -- /bin/sh -c 'printf pwned > \"$1\"' sh "+strconv.Quote(deniedWrite))
		testutil.FailErr(t, "phase 2b one-shot sibling write", err)
		if decodeOutcome(t, out).Status == 0 {
			t.Fatalf("phase 2b one-shot write grant leaked to sibling %s", deniedWrite)
		}
	}
	for _, deniedRead := range []string{privKey, pubKey, otherKey} {
		out, err = callWithApprovedAccess(oneShotWriteGrant, "external -- /bin/cat "+strconv.Quote(deniedRead))
		testutil.FailErr(t, "phase 2b one-shot write grant read attempt", err)
		if decodeOutcome(t, out).Status == 0 {
			t.Fatalf("phase 2b write-only one-shot grant allowed reading sibling %s", deniedRead)
		}
	}
	testutil.FailErr(t, "restore authorized_keys", os.WriteFile(authKeys, []byte("ssh-ed25519 INITIAL\n"), 0o600))

	// Phase 2c: Subsequent unapproved invocation after one-shot ApprovedFileAccess — one-shot grant does not persist.
	out, err = f.call(t, t.Context(), "external -- /bin/cat "+strconv.Quote(privKey))
	testutil.FailErr(t, "phase 2c unapproved read after one-shot", err)
	if decodeOutcome(t, out).Status == 0 {
		t.Fatal("phase 2c one-shot read grant persisted to subsequent unapproved call")
	}
	out, err = f.call(t, t.Context(), "external -- /bin/sh -c 'printf after-oneshot > \"$1\"' sh "+strconv.Quote(authKeys))
	testutil.FailErr(t, "phase 2c unapproved write after one-shot", err)
	if decodeOutcome(t, out).Status == 0 {
		t.Fatal("phase 2c one-shot write grant persisted to subsequent unapproved call")
	}

	// Phase 3: Chat-scoped exact read overlay on id_ed25519 — only id_ed25519 is readable; id_rsa, id_ed25519.pub, authorized_keys and writes stay denied.
	readOverlay = []string{privKey}
	out, err = f.call(t, t.Context(), "external -- /bin/cat "+strconv.Quote(privKey))
	testutil.FailErr(t, "phase 3 overlay granted read", err)
	got = decodeOutcome(t, out)
	if got.Status != 0 || !strings.Contains(got.Stdout, "PRIVATE-ED25519") {
		t.Fatalf("phase 3 overlay granted read of id_ed25519 failed: %+v", got)
	}
	for _, deniedRead := range []string{otherKey, pubKey, authKeys} {
		out, err = f.call(t, t.Context(), "external -- /bin/cat "+strconv.Quote(deniedRead))
		testutil.FailErr(t, "phase 3 overlay sibling read", err)
		if decodeOutcome(t, out).Status == 0 {
			t.Fatalf("phase 3 overlay read grant leaked to sibling %s", deniedRead)
		}
	}
	out, err = f.call(t, t.Context(), "external -- /bin/sh -c 'printf pwned > \"$1\"' sh "+strconv.Quote(authKeys))
	testutil.FailErr(t, "phase 3 overlay write without write grant", err)
	if decodeOutcome(t, out).Status == 0 {
		t.Fatal("phase 3 overlay read grant allowed writing authorized_keys")
	}

	// Phase 4: Chat-scoped exact write overlay on authorized_keys and authorized_keys_test — only those targets are writable; id_ed25519/evil.txt writes and id_rsa reads fail.
	readOverlay = nil
	writeOverlay = []string{authKeys, authKeysTest}
	out, err = f.call(t, t.Context(), "external -- /bin/sh -c 'printf approved-key > \"$1\" && printf approved-test > \"$2\"' sh "+strconv.Quote(authKeys)+" "+strconv.Quote(authKeysTest))
	testutil.FailErr(t, "phase 4 overlay granted write", err)
	if got = decodeOutcome(t, out); got.Status != 0 {
		t.Fatalf("phase 4 overlay granted write to authorized_keys/authorized_keys_test failed: %+v", got)
	}
	for _, deniedWrite := range []string{privKey, filepath.Join(sshDir, "evil.txt")} {
		out, err = f.call(t, t.Context(), "external -- /bin/sh -c 'printf pwned > \"$1\"' sh "+strconv.Quote(deniedWrite))
		testutil.FailErr(t, "phase 4 overlay sibling write", err)
		if decodeOutcome(t, out).Status == 0 {
			t.Fatalf("phase 4 overlay write grant leaked to sibling %s", deniedWrite)
		}
	}
	out, err = f.call(t, t.Context(), "external -- /bin/cat "+strconv.Quote(otherKey))
	testutil.FailErr(t, "phase 4 overlay ungranted read", err)
	if decodeOutcome(t, out).Status == 0 {
		t.Fatal("phase 4 overlay write grant allowed reading id_rsa")
	}

	// Phase 5: Revoked — reading id_ed25519 and writing authorized_keys/authorized_keys_test fail again while @scratch stays writable.
	readOverlay = nil
	writeOverlay = nil
	out, err = f.call(t, t.Context(), "external -- /bin/cat "+strconv.Quote(privKey))
	testutil.FailErr(t, "phase 5 revoked read", err)
	if decodeOutcome(t, out).Status == 0 {
		t.Fatal("phase 5 revoked read grant still allowed reading id_ed25519")
	}
	for _, revokedWrite := range []string{authKeys, authKeysTest} {
		out, err = f.call(t, t.Context(), "external -- /bin/sh -c 'printf after-revoke > \"$1\"' sh "+strconv.Quote(revokedWrite))
		testutil.FailErr(t, "phase 5 revoked write", err)
		if decodeOutcome(t, out).Status == 0 {
			t.Fatalf("phase 5 revoked write grant still allowed writing %s", revokedWrite)
		}
	}
	if data, err := os.ReadFile(authKeys); err != nil || string(data) != "approved-key" {
		t.Fatalf("authorized_keys after revoke = %q, %v; want \"approved-key\"", data, err)
	}
	if data, err := os.ReadFile(authKeysTest); err != nil || string(data) != "approved-test" {
		t.Fatalf("authorized_keys_test after revoke = %q, %v; want \"approved-test\"", data, err)
	}
}
