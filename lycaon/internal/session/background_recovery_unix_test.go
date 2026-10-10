//go:build unix

package session_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBackgroundRecoveryPreservesSessionOwnershipAndCompletedOutput(t *testing.T) {
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	registry.Output.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	t.Cleanup(func() { _ = registry.Lifecycle.Close(t.Context()) })
	manager := session.NewHost(store.NewMemory(), session.Models{Limits: settings.DefaultSessionLimits()}, nil)
	manager.SetBackgroundRegistry(registry)
	handle, err := registry.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "owner", ProjectID: "project", Mode: bgprocess.JobModeBackground,
		Runner: hostcmd.NewRunner(), Request: hostcmd.Request{
			Launch: exec.HostLaunch("background recovery fixture"), ProjectDir: t.TempDir(),
			Stages: []exec.Stage{{Name: "sh", Args: []string{"-c", "printf recovered-output; sleep 30"}}},
		},
	})
	testutil.FailErr(t, "start recoverable process", err)
	testutil.WaitFor(t, 5*time.Second, func() bool {
		output, readErr := manager.Processes.GetBackgroundProcessOutput(t.Context(), "owner", handle)
		return readErr == nil && len(output.Chunks) > 0
	})
	processes := manager.Processes.ListBackgroundProcesses(t.Context(), "owner")
	if len(processes) != 1 || processes[0].ProcessID != handle || processes[0].Output == nil {
		t.Fatalf("recovery omitted owned process output: %+v", processes)
	}
	if len(manager.Processes.ListBackgroundProcesses(t.Context(), "other")) != 0 {
		t.Fatal("recovery listed another session's process")
	}
	_, err = manager.Processes.GetBackgroundProcessOutput(t.Context(), "other", handle)
	if !errors.Is(err, bgprocess.ErrProcessNotFound) {
		t.Fatalf("foreign read = %v", err)
	}
	_, err = manager.Processes.StopBackgroundProcess("other", handle)
	if !errors.Is(err, bgprocess.ErrProcessNotFound) {
		t.Fatalf("foreign stop = %v", err)
	}
	if !registry.HasRunning("owner") {
		t.Fatal("foreign stop terminated the owner's process")
	}
	_, err = manager.Processes.StopBackgroundProcess("owner", handle)
	testutil.FailErr(t, "stop owned recovered process", err)
	done, err := registry.Lifecycle.Await(t.Context(), "owner", handle, 5*time.Second)
	testutil.FailErr(t, "await recovered process settlement", err)
	if !done {
		t.Fatal("recovered process did not settle")
	}
	output, err := manager.Processes.GetBackgroundProcessOutput(t.Context(), "owner", handle)
	testutil.FailErr(t, "recover completed output", err)
	var text strings.Builder
	for _, chunk := range output.Chunks {
		text.WriteString(chunk.Text)
	}
	if output.Running || !strings.Contains(text.String(), "recovered-output") {
		t.Fatalf("completed recovery lost output or retained running state: %+v", output)
	}
}
