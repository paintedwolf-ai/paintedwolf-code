package gitexec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
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

func TestGitStagingUsesOnlyTheAdmittedActionWorkspace(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "host"))
	t.Setenv("TMPDIR", t.TempDir())
	agent := AgentEntry(t.Context())
	if _, err := MakeScratch(agent, "index-*"); !errors.Is(err, ErrLaunchAuthorityRequired) {
		t.Fatalf("unadmitted staging: %v", err)
	}
	plan := exec.AgentLaunch(exec.LaunchAgentCommand, "git", nil)
	if _, err := MakeScratch(WithAgentLaunch(agent, AgentLaunch{Plan: plan}), "index-*"); err == nil {
		t.Fatal("agent staging used machine temp without a private workspace")
	}
	workspace, err := confine.NewActionWorkspace()
	testutil.FailErr(t, "prepare private workspace", err)
	location, err := MakeScratch(WithAgentLaunch(agent, AgentLaunch{Plan: plan, Workspace: workspace}), "index-*")
	testutil.FailErr(t, "stage private index", err)
	if filepath.Dir(location) != workspace.Path() {
		t.Fatalf("staging parent %q, want %q", filepath.Dir(location), workspace.Path())
	}
	host, err := MakeScratch(WithHostPurpose(agent, "repository_lease"), "index-*")
	testutil.FailErr(t, "stage host bookkeeping", err)
	if filepath.Dir(host) != os.TempDir() {
		t.Fatalf("host staging location %q", host)
	}
}
