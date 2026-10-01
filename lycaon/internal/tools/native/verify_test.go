package native

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestVerifyTool(t *testing.T) {
	runner := hostcmd.NewRunner()

	tool := &VerifyTool{Runner: runner, Background: bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})}
	dir := t.TempDir()
	ctx := nativefixture.AgentContext(dir, "implement")
	out, err := tool.Run(context.Background(), map[string]any{"command": "true"}, ctx)
	testutil.FailErr(t, "VerifyTool.Run", err)
	if out == "" {
		t.Fatal("expected non-empty verify result")
	}
}
