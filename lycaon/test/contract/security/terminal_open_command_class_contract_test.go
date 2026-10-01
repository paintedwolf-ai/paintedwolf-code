package contract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// TestTerminalOpenClassifyTierParityWithCommand locks command-class irreversible
// floor plus path-escape behavior for terminal_open.
func TestTerminalOpenClassifyTierParityWithCommand(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	cases := []struct {
		cmd string
	}{
		{"go test ./..."},
		{"git push origin main"},
		{"git push --force"},
		{"cat /etc/passwd"},
		{"cat ~/.ssh/id_rsa"},
		{"dd if=/dev/zero of=x"},
	}
	for _, tc := range cases {
		commandTier := settings.ClassifyTier(hitl.ProposedAction{
			Tool: "command", ProjectDir: proj, Args: map[string]any{"command": tc.cmd},
		})
		openTier := settings.ClassifyTier(hitl.ProposedAction{
			Tool: "terminal_open", ProjectDir: proj, Args: map[string]any{"command": tc.cmd},
		})
		if commandTier != openTier {
			t.Fatalf("command %q: command=%v terminal_open=%v", tc.cmd, commandTier, openTier)
		}
	}
}

func TestBundledTerminalOpenBranchesOnContainmentNotCommandText(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	contained := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{dir}}
	for _, command := range []string{"go test ./...", "cat /etc/hosts", "git push origin main"} {
		approved := evalBundled(t, hitl.ProposedAction{
			Tool: "terminal_open", Args: map[string]any{"command": command}, ProjectDir: dir,
			Contained: contained,
		})
		if !approved.AutoApproved() || approved.Required() || approved.Denied {
			t.Errorf("contained terminal_open %q must run: %+v", command, approved)
		}
		asked := evalBundled(t, hitl.ProposedAction{
			Tool: "terminal_open", Args: map[string]any{"command": command}, ProjectDir: dir,
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
		SessionID: "s", ProjectID: "p",
		Roots: []projectroot.RootRef{{ID: "main", Path: dir, IsPrimary: true}},
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
	var rej *tools.ToolReject
	if !errors.As(err, &rej) || rej.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("err = %v, want TOOL_ARGS_INVALID for pipeline", err)
	}
}

func TestTerminalOpenNotArgvRejectObservation(t *testing.T) {
	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	reg := tools.NewDefaultRegistry()
	contractcheck.FailErr(t, "register", native.RegisterTerminalSessionTools(reg, bg))

	exec := tools.NewDefaultToolExecutor(nil, reg, "implement")

	_, err := exec.Invoke(context.Background(), "terminal_open", map[string]any{"command": "go test; rm -rf /"}, tools.ToolContext{
		SessionID:    "sess",
		ProjectID:    "proj",
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "implement",
	})
	if err == nil {
		t.Fatal("expected argv deny")
	}
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_NOT_ARGV" {
		t.Fatalf("want COMMAND_NOT_ARGV ToolReject, got %v", err)
	}
}

func TestTerminalOpenWiringUsesHostcmdArgv(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "tools", "native", "terminal", "session_open.go")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read terminal/session_open.go", err)
	text := string(data)
	for _, marker := range []string{
		"hostcmd.Request",
		"tctx.CommandPlan",
		"ErrShellMetacharacters",
		"pipeline_not_supported",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("%s missing %q", path, marker)
		}
	}
}
