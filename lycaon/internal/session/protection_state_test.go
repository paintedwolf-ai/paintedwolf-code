package session_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/protection"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildProtectionStateUnknownVsActive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based fixture is unix-oriented")
	}
	reg := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	dir := t.TempDir()
	handle, err := reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
		SessionID: "sess-prot", ProjectID: "proj",
		Request: hostcmd.Request{
			ProjectDir: dir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
			Launch: exec.AgentLaunch(exec.LaunchAgentCommand, "protection test", &confine.Confinement{
				Roots:   []string{dir},
				Network: confine.NetworkDirectIP,
			}),
		},
		Runner:     runner,
		Mode:       bgprocess.JobModeBackground,
		ToolCallID: "call-direct-1",
	})
	testutil.FailErr(t, "start", err)
	t.Cleanup(func() { _, _ = reg.Stop("sess-prot", handle) })

	prot := protection.BuildProtectionState("sess-prot", reg)
	if prot == nil || len(prot.BackgroundDirect) != 1 {
		t.Fatalf("prot = %+v", prot)
	}
	if prot.BackgroundDirect[0].Status != api.SessionBackgroundDirectStatusActive {
		t.Fatalf("status = %s", prot.BackgroundDirect[0].Status)
	}
	if prot.BackgroundDirect[0].ToolCallID != "call-direct-1" {
		t.Fatalf("tool_call_id = %q", prot.BackgroundDirect[0].ToolCallID)
	}

	testutil.FailErr(t, "mark unknown", reg.MarkDirectIPLivenessUnknown("sess-prot", handle))
	prot = protection.BuildProtectionState("sess-prot", reg)
	if prot.BackgroundDirect[0].Status != api.SessionBackgroundDirectStatusUnknown {
		t.Fatalf("want unknown, got %s", prot.BackgroundDirect[0].Status)
	}
}

func TestProtectionStateReconstructEmitsOnceWithoutDestinations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based fixture is unix-oriented")
	}
	reg := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	dir := t.TempDir()
	handle, err := reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
		SessionID: "sess-rec", ProjectID: "proj",
		Request: hostcmd.Request{
			ProjectDir: dir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
			Launch: exec.AgentLaunch(exec.LaunchAgentCommand, "reconstruct test", &confine.Confinement{
				Roots:   []string{dir},
				Network: confine.NetworkDirectIP,
			}),
		},
		Runner: runner,
		Mode:   bgprocess.JobModeBackground, ToolCallID: "call-9",
	})
	testutil.FailErr(t, "start", err)
	t.Cleanup(func() { _, _ = reg.Stop("sess-rec", handle) })

	mgr := session.NewHost(sessionstore.NewMemory(), session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetBackgroundRegistry(reg)
	var emits atomic.Int32
	mgr.Chats.Protection.SetDirectIPReconstructHook(func(sessionID, gotHandle, toolCallID string) {
		emits.Add(1)
		if sessionID != "sess-rec" || gotHandle != handle || toolCallID != "call-9" {
			t.Errorf("unexpected reconstruct args: %s %s %s", sessionID, gotHandle, toolCallID)
		}
	})

	_ = mgr.Chats.Protection.ProtectionStateForSession("sess-rec")
	_ = mgr.Chats.Protection.ProtectionStateForSession("sess-rec")
	if got := emits.Load(); got != 1 {
		t.Fatalf("reconstruct emits = %d want 1", got)
	}

	var replacementEmits atomic.Int32
	mgr.Chats.Protection.SetDirectIPReconstructHook(func(_, _, _ string) {
		replacementEmits.Add(1)
	})
	_ = mgr.Chats.Protection.ProtectionStateForSession("sess-rec")
	if got := replacementEmits.Load(); got != 1 {
		t.Fatalf("replacement hook emits = %d want 1", got)
	}
}

func TestRevokeChatGrantAfterRecoveryKeepsBackgroundWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based fixture is unix-oriented")
	}
	reg := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	dir := t.TempDir()
	handle, err := reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
		SessionID: "sess-rev", ProjectID: "proj",
		Request: hostcmd.Request{
			ProjectDir: dir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
			Launch: exec.AgentLaunch(exec.LaunchAgentCommand, "revocation test", &confine.Confinement{
				Roots:   []string{dir},
				Network: confine.NetworkDirectIP,
			}),
		},
		Runner: runner,
		Mode:   bgprocess.JobModeBackground,
	})
	testutil.FailErr(t, "start", err)
	t.Cleanup(func() { _, _ = reg.Stop("sess-rev", handle) })

	rt := approvalstate.NewSocketCapabilityRuntime()
	approved, resolved := tempSocket(t)
	rt.GrantChat("sess-rev", confine.SocketGrant{ApprovedPath: approved, ResolvedPath: resolved}, "grant_t", "cp", "d", nil)
	if len(rt.AppliedGrants("sess-rev")) != 1 {
		t.Fatal("expected live chat grant")
	}
	if _, ok := rt.RevokeByID("grant_t"); !ok {
		t.Fatal("revoke found no grant")
	}
	if len(rt.AppliedGrants("sess-rev")) != 0 {
		t.Fatal("revoke must affect next boundary")
	}
	prot := protection.BuildProtectionState("sess-rev", reg)
	if prot == nil || len(prot.BackgroundDirect) != 1 {
		t.Fatalf("still-running direct process must keep warning, prot=%+v", prot)
	}
}

func TestProtectionStateReportsStartupOverrides(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "off")
	root := t.TempDir()
	t.Setenv("LYCAON_SANDBOX_WRITE_ROOTS", root)
	prot := protection.BuildProtectionState("session", nil)
	if prot == nil || !prot.ControlPlaneReadsAllowed || len(prot.AdditionalWriteRoots) != 1 {
		t.Fatalf("startup overrides = %+v", prot)
	}
	if prot.SandboxBypass {
		t.Fatal("partial overrides reported as full bypass")
	}
	if len(confine.SecretReadDenyRoots()) != 0 {
		t.Fatal("read override disagrees with enforced read roots")
	}
}

// tempSocket listens on a short unix socket path and returns its approved and resolved paths.
func tempSocket(t *testing.T) (approved, resolved string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sk") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen unix", err)
	t.Cleanup(func() { _ = ln.Close() })
	resolved, err = filepath.EvalSymlinks(path)
	testutil.FailErr(t, "eval symlinks", err)
	return path, resolved
}
