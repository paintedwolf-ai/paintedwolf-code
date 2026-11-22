package gitexec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/exec"
)

// Inside an agent's tool call Git runs only under the agent's admitted launch
// or a named host purpose; nothing falls back to host authority by omission.
func TestAgentEntryRequiresAnExplicitLaunchAuthority(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	agent := AgentEntry(t.Context())

	if _, _, err := Run(agent, dir, []string{"status", "--porcelain"}, Opts{}); !errors.Is(err, ErrLaunchAuthorityRequired) {
		t.Fatalf("agent entry without a launch: err = %v, want ErrLaunchAuthorityRequired", err)
	}
	if out, code, err := Run(WithHostPurpose(agent, "source_snapshot"), dir, []string{"status", "--porcelain"}, Opts{}); err != nil || code != 0 {
		t.Fatalf("named host purpose: code=%d err=%v out=%s", code, err, out)
	}
	if _, ok := AgentLaunchFrom(WithHostPurpose(agent, "source_snapshot")); ok {
		t.Fatal("a host purpose reported an agent launch")
	}

	plan := exec.AgentLaunch(exec.LaunchAgentCommand, "git", nil)
	launched := WithAgentLaunch(agent, AgentLaunch{Plan: plan})
	got, err := launchPlan(launched, "git")
	if err != nil || got.Kind != exec.LaunchAgentCommand || got.Subject != "git" {
		t.Fatalf("agent launch plan = %+v, %v", got, err)
	}
	// A host purpose inside an agent launch keeps that launch.
	if got, err := launchPlan(WithHostPurpose(launched, "repository_lease"), "git"); err != nil || got.Kind != exec.LaunchAgentCommand {
		t.Fatalf("host purpose replaced the agent launch: %+v, %v", got, err)
	}
	// A nested tool call starts from its own entry, not its caller's launch.
	if _, err := launchPlan(AgentEntry(launched), "git"); !errors.Is(err, ErrLaunchAuthorityRequired) {
		t.Fatalf("nested entry inherited the caller's launch: %v", err)
	}
	if got, err := launchPlan(t.Context(), "git"); err != nil || got.Kind != exec.LaunchHostInternal {
		t.Fatalf("host caller plan = %+v, %v", got, err)
	}
}

// Git's working files go where the agent's launch can write them: its scratch,
// not the host's temporary directory.
func TestMkdirTempFollowsTheAgentLaunchScratch(t *testing.T) {
	scratch := t.TempDir()
	launched := WithAgentLaunch(AgentEntry(t.Context()), AgentLaunch{Plan: exec.AgentLaunch(exec.LaunchAgentCommand, "git", nil), Scratch: scratch})
	dir, err := MkdirTemp(launched, "work-*")
	if err != nil || filepath.Dir(dir) != scratch {
		t.Fatalf("agent launch working dir = %q (%v), want one inside %q", dir, err, scratch)
	}
	host, err := MkdirTemp(t.Context(), "work-*")
	t.Cleanup(func() { _ = os.RemoveAll(host) })
	if err != nil || filepath.Dir(host) != filepath.Clean(os.TempDir()) {
		t.Fatalf("host working dir = %q (%v), want one inside %q", host, err, os.TempDir())
	}
}
