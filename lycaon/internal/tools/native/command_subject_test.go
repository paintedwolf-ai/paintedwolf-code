package native

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestCommandSubjectSurvivesProcessExit(t *testing.T) {
	registry := newTestBackgroundRegistry(t)
	executable, err := os.Executable()
	testutil.FailErr(t, "locate command fixture", err)
	argument := "-test.list=^TestCommandSubjectSurvivesProcessExit$"
	handle, err := registry.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "session", ProjectID: "project", Mode: bgprocess.JobModeBackground, Runner: hostcmd.NewRunner(),
		Request: hostcmd.Request{Launch: exec.HostLaunch("command presentation test"), ProjectDir: t.TempDir(), Stages: []exec.Stage{{Name: executable, Args: []string{argument}}}},
	})
	testutil.FailErr(t, "start command", err)
	t.Cleanup(func() { _, _ = registry.Stop("session", handle) })
	done, err := registry.Await(t.Context(), "session", handle, 5*time.Second)
	testutil.FailErr(t, "await command", err)
	if !done {
		t.Fatal("command did not exit")
	}
	for _, tool := range []interface {
		Run(context.Context, map[string]any, tools.ToolContext) (string, error)
	}{
		&CommandOutputTool{Registry: registry}, &CommandStopTool{Registry: registry},
	} {
		captured := &tools.ToolInvocationOut{}
		_, err := tool.Run(t.Context(), map[string]any{"handle": handle, "cursor": float64(0)}, tools.ToolContext{SessionID: "session", Out: captured})
		testutil.FailErr(t, "read completed command target", err)
		if !strings.Contains(captured.DisplaySubject, argument) || strings.Contains(captured.DisplaySubject, handle) {
			t.Fatalf("target = %q", captured.DisplaySubject)
		}
		other := &tools.ToolInvocationOut{}
		_, err = tool.Run(t.Context(), map[string]any{"handle": handle, "cursor": float64(0)}, tools.ToolContext{SessionID: "other", Out: other})
		if err == nil || other.DisplaySubject != "" {
			t.Fatal("command target crossed session ownership")
		}
	}
}
