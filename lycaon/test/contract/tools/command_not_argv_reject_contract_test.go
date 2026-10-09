package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func commandRejectExecutor(t *testing.T) *tools.DefaultToolExecutor {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	contractcheck.FailErr(t, "Register command", reg.Register("command", (&command.CommandTool{Runner: hostcmd.NewRunner()}).Run))
	return tools.NewDefaultToolExecutor(nil, reg, "implement")
}

func TestCommandNotArgvRejectObservation(t *testing.T) {
	t.Parallel()
	exec := commandRejectExecutor(t)
	// Substitution is rejected because expanded values are not visible in the command line.
	_, err := exec.Invoke(context.Background(), "command", map[string]any{"command": "go test $(id -u)"}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "implement",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_NOT_ARGV" {
		t.Fatalf("want COMMAND_NOT_ARGV ToolReject, got %v", err)
	}
}

// Sequencing operators execute as separate processes; substitution and background operators are rejected.
func TestCommandSurfaceAcceptsSequencingRejectsSubstitution(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		"go test ./... && go vet ./...",
		"which go || echo missing",
		"go build; go test",
	} {
		plan, err := commandsurface.ParsePlan(map[string]any{"command": command})
		if err != nil {
			t.Fatalf("sequenced command %q rejected: %v", command, err)
		}
		if len(plan.Stages) < 2 {
			t.Fatalf("command %q produced %d stages, want one per program", command, len(plan.Stages))
		}
	}
	for _, command := range []string{
		"go build $(git rev-parse HEAD)",
		"echo `whoami`",
		"python3 -m http.server &",
		"cd pkg && go test ./...",
	} {
		if _, err := commandsurface.ParsePlan(map[string]any{"command": command}); err == nil {
			t.Fatalf("command %q must be rejected", command)
		}
	}
}

func TestCommandNotArgvRejectsShellPipeline(t *testing.T) {
	t.Parallel()
	exec := commandRejectExecutor(t)
	_, err := exec.Invoke(context.Background(), "command", map[string]any{
		"command":          "git log --oneline | head -20",
		"terminal_capture": true,
	}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "implement",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_NOT_ARGV" {
		t.Fatalf("want COMMAND_NOT_ARGV ToolReject, got %v", err)
	}
	if strings.Contains(err.Error(), "Allowed patterns") {
		t.Fatalf("reject must not dump patterns, got %q", err.Error())
	}
}

func TestCommandNotArgvRejectsInlineEnvironment(t *testing.T) {
	t.Parallel()
	exec := commandRejectExecutor(t)
	_, err := exec.Invoke(context.Background(), "command", map[string]any{
		"command": "TOKEN=value",
	}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "implement",
	})
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_NOT_ARGV" {
		t.Fatalf("want COMMAND_NOT_ARGV ToolReject, got %v", err)
	}
	if !strings.Contains(err.Error(), "environment assignments belong in env") {
		t.Fatalf("reject does not direct the agent to env: %v", err)
	}
}

func TestCommandAcceptsPrefixEnvironment(t *testing.T) {
	t.Parallel()
	plan, err := commandsurface.ParsePlan(map[string]any{
		"command": "TOKEN=value echo ok",
	})
	if err != nil {
		t.Fatalf("prefix environment command rejected: %v", err)
	}
	stages := plan.Stages
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	if stages[0].Env["TOKEN"] != "value" {
		t.Fatalf("expected TOKEN=value, got %v", stages[0].Env)
	}
	if stages[0].Name != "echo" {
		t.Fatalf("expected name 'echo', got %q", stages[0].Name)
	}
	if len(stages[0].Args) != 1 || stages[0].Args[0] != "ok" {
		t.Fatalf("expected args ['ok'], got %v", stages[0].Args)
	}
}

func TestCommandArgvRequiredRejectObservation(t *testing.T) {
	t.Parallel()
	exec := commandRejectExecutor(t)
	_, err := exec.Invoke(context.Background(), "command", map[string]any{
		"cwd": ".", "timeout_ms": 120000, "wait_ms": 30000,
	}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "implement",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_ARGV_REQUIRED" {
		t.Fatalf("want COMMAND_ARGV_REQUIRED ToolReject, got %v", err)
	}
}

func TestCommandArgvConflictRejectObservation(t *testing.T) {
	t.Parallel()
	exec := commandRejectExecutor(t)
	_, err := exec.Invoke(context.Background(), "command", map[string]any{
		"command":  "go version",
		"pipeline": []any{"echo hi"},
	}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "implement",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_ARGV_CONFLICT" {
		t.Fatalf("want COMMAND_ARGV_CONFLICT ToolReject, got %v", err)
	}
}
