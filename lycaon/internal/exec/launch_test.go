package exec

import (
	"os/exec"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
)

func TestLaunchPlanRequiresActionLeaseForProxyConfinement(t *testing.T) {
	c := &confine.Confinement{
		Network: confine.NetworkProxyOnly,
	}
	plan := AgentLaunch(LaunchAgentCommand, "fixture", c)
	if err := plan.validate(); err == nil {
		t.Fatal("unbound proxy confinement was accepted")
	}
	restore := confine.SetBrokerForTest(nil, egressproxy.Addrs{HTTP: "127.0.0.1:1", SOCKS: "127.0.0.1:2"})
	defer restore()
	lease, err := confine.BindAction(c, confine.EgressCommand{ToolCallID: "launch-test"})
	if err != nil {
		t.Fatalf("bind egress: %v", err)
	}
	defer lease.Close(t.Context())
	if err := plan.validate(); err != nil {
		t.Fatalf("bound proxy confinement rejected: %v", err)
	}
}

func TestLaunchPlanIsMandatory(t *testing.T) {
	if err := (LaunchPlan{}).validate(); err == nil {
		t.Fatal("empty launch plan accepted")
	}
}

func TestLaunchPlanRequiresSubjectAndSeparatesHostAuthority(t *testing.T) {
	if err := (LaunchPlan{Kind: LaunchHostInternal}).validate(); err == nil {
		t.Fatal("subjectless host launch accepted")
	}
	if err := (LaunchPlan{
		Kind: LaunchHostInternal, Subject: "host",
		Confinement: &confine.Confinement{Network: confine.NetworkDeny},
	}).validate(); err == nil {
		t.Fatal("host launch accepted agent confinement")
	}
}

func TestExternalScannerLaunchForbidsConfinement(t *testing.T) {
	if err := ExternalScannerLaunch("trivy").validate(); err != nil {
		t.Fatalf("unconfined external scanner rejected: %v", err)
	}
	if err := (LaunchPlan{
		Kind: LaunchExternalScanner, Subject: "trivy",
		Confinement: &confine.Confinement{Network: confine.NetworkDeny},
	}).validate(); err == nil {
		t.Fatal("confined external scanner launch accepted")
	}
}

func TestExternalScannerLaunchIsValidWhileEnforcing(t *testing.T) {
	if !confine.Available() {
		t.Skip("OS confinement unavailable")
	}
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	if err := ExternalScannerLaunch("trivy").validate(); err != nil {
		t.Fatalf("external scanner launch while enforcing: %v", err)
	}
	if err := BundledScannerLaunch("opengrep", nil).validate(); err == nil {
		t.Fatal("unconfined bundled scanner launch accepted while enforcing")
	}
}

func TestStartPTYRejectsMissingLaunchPlan(t *testing.T) {
	if _, err := StartPTY(t.Context(), "echo", []string{"hello"}, PTYOpts{}); err == nil {
		t.Fatal("StartPTY accepted a missing launch plan")
	}
}

func TestConfinedCommandEnvDisablesGitFsmonitorByDefault(t *testing.T) {
	cmd := &exec.Cmd{}
	confinement := &confine.Confinement{Network: confine.NetworkDeny}
	if err := configureCmdEnv(cmd, ExecOpts{
		Launch: AgentLaunch(LaunchAgentCommand, "git status", confinement),
		Env:    []string{"PATH=/usr/bin"},
	}); err != nil {
		t.Fatalf("configureCmdEnv: %v", err)
	}
	for _, want := range []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.fsmonitor",
		"GIT_CONFIG_VALUE_0=false",
	} {
		if !slices.Contains(cmd.Env, want) {
			t.Fatalf("confined env %v missing %q", cmd.Env, want)
		}
	}

	hostCmd := &exec.Cmd{}
	hostConfinement := &confine.Confinement{Network: confine.NetworkDeny, HostExecution: true}
	if err := configureCmdEnv(hostCmd, ExecOpts{
		Launch: AgentLaunch(LaunchAgentCommand, "git status", hostConfinement),
		Env:    []string{"PATH=/usr/bin"},
	}); err != nil {
		t.Fatalf("configureCmdEnv host execution: %v", err)
	}
	if slices.Contains(hostCmd.Env, "GIT_CONFIG_COUNT=1") {
		t.Fatalf("host-execution env unexpectedly set GIT_CONFIG_COUNT: %v", hostCmd.Env)
	}

	for _, existing := range []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_PARAMETERS='core.fsmonitor=true'",
	} {
		customCmd := &exec.Cmd{}
		if err := configureCmdEnv(customCmd, ExecOpts{
			Launch:    AgentLaunch(LaunchAgentCommand, "git status", confinement),
			Env:       []string{"PATH=/usr/bin"},
			AppendEnv: []string{existing},
		}); err != nil {
			t.Fatalf("configureCmdEnv with %s: %v", existing, err)
		}
		if slices.Contains(customCmd.Env, "GIT_CONFIG_KEY_0=core.fsmonitor") {
			t.Fatalf("caller override %q was clobbered: %v", existing, customCmd.Env)
		}
	}
}
