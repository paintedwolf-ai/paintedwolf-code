package contract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestTerminalOpenClassifyTierParityWithCommand: command and terminal_open
// take the same tier from containment, never from the command text.
func TestTerminalOpenClassifyTierParityWithCommand(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	contained := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj}}
	cases := []struct {
		cmd       string
		contained hitl.Contained
		want      settings.ReversibilityTier
	}{
		{"go test ./...", contained, settings.TierRecoverable},
		{"git push --force", contained, settings.TierRecoverable},
		{"cat ~/.ssh/id_rsa", contained, settings.TierRecoverable},
		{"dd if=/dev/zero of=x", contained, settings.TierRecoverable},
		{"go test ./...", hitl.Contained{}, settings.TierIrreversible},
		{"git push origin main", hitl.Contained{}, settings.TierIrreversible},
		{"cat /etc/passwd", hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressDirectIP, Roots: []string{proj}}, settings.TierIrreversible},
	}
	for _, tc := range cases {
		for _, tool := range []string{"command", "terminal_open"} {
			got := settings.ClassifyTier(hitl.ProposedAction{
				Invocation: hitl.ActionInvocation{Tool: tool, Args: map[string]any{"command": tc.cmd}},
				Scope:      hitl.ActionScope{ProjectDir: proj},
				Execution:  hitl.ActionExecution{Contained: tc.contained},
			})
			if got != tc.want {
				t.Errorf("%s %q contained=%+v: tier %s, want %s", tool, tc.cmd, tc.contained,
					settings.TierLabel(got), settings.TierLabel(tc.want))
			}
		}
	}
}

func TestBundledTerminalOpenBranchesOnContainmentNotCommandText(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	contained := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{dir}}
	for _, command := range []string{"go test ./...", "cat /etc/hosts", "git push origin main"} {
		approved := evalBundled(t, hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "terminal_open",
				Args: map[string]any{"command": command},
			},
			Scope: hitl.ActionScope{
				ProjectDir: dir,
			},
			Execution: hitl.ActionExecution{
				Contained: contained,
			},
		})
		if !approved.AutoApproved() || approved.Required() || approved.Denied {
			t.Errorf("contained terminal_open %q must run: %+v", command, approved)
		}
		asked := evalBundled(t, hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "terminal_open",
				Args: map[string]any{"command": command},
			},
			Scope: hitl.ActionScope{
				ProjectDir: dir,
			},
		})
		if !asked.Required() || asked.Denied {
			t.Errorf("uncontained terminal_open %q must ask: %+v", command, asked)
		}
	}
}

func TestTerminalOpenRejectsShellStringAndPipeline(t *testing.T) {
	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	reg := tools.NewDefaultRegistry()
	contractcheck.FailErr(t, "register", native.RegisterTerminalSessionTools(reg, bg))
	dir := t.TempDir()
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "s",
			ProjectID: "p"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "main", Path: dir, IsPrimary: true}}},
	}
	// Substitution never expands, so it stays a metacharacter rejection.
	_, err := reg.Run(context.Background(), "terminal_open", map[string]any{
		"command": "echo $(whoami)",
	}, tctx)
	if err == nil {
		t.Fatal("expected metachar reject")
	}
	if !errors.Is(err, argv.ErrShellMetacharacters) && !strings.Contains(err.Error(), "metachar") {
		t.Fatalf("err = %v, want shell metachar", err)
	}
	// A sequence parses, but a pty holds one process, so it is refused as
	// composition — still a command-surface error, and still never run.
	_, err = reg.Run(context.Background(), "terminal_open", map[string]any{
		"command": "echo hi; rm -rf /tmp/x",
	}, tctx)
	if !errors.Is(err, commandsurface.ErrSequenceUnsupported) {
		t.Fatalf("err = %v, want ErrSequenceUnsupported", err)
	}
	_, err = reg.Run(context.Background(), "terminal_open", map[string]any{
		"command":  "printf hi",
		"pipeline": []any{map[string]any{"command": "cat"}},
	}, tctx)
	if err == nil {
		t.Fatal("expected pipeline reject")
	}
	var rej *toolrejection.ToolReject
	if !errors.As(err, &rej) || rej.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("err = %v, want TOOL_ARGS_INVALID for pipeline", err)
	}
}

func TestTerminalOpenNotArgvRejectObservation(t *testing.T) {
	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	reg := tools.NewDefaultRegistry()
	contractcheck.FailErr(t, "register", native.RegisterTerminalSessionTools(reg, bg))

	exec := toolexecution.NewExecutor(nil, reg, "implement")

	_, err := exec.Invoke(context.Background(), "terminal_open", map[string]any{"command": "go test; rm -rf /"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess",
			ProjectID: "proj",
			Agent:     "implement"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
	})
	if err == nil {
		t.Fatal("expected argv deny")
	}
	tr := toolrejection.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_NOT_ARGV" {
		t.Fatalf("want COMMAND_NOT_ARGV ToolReject, got %v", err)
	}
}
