package platform_test

import (
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBoardHostUsesRuntimePlatform(t *testing.T) {
	platform.SetBoardHostOverride(nil)
	host := platform.BoardHost(api.ExecutionTargetLocal)
	if host == nil {
		t.Fatal("expected host facts")
	}
	if host.OS != runtime.GOOS || host.Arch != runtime.GOARCH {
		t.Fatalf("host = %+v want %s/%s", host, runtime.GOOS, runtime.GOARCH)
	}
	if host.ExecutionTarget != api.ExecutionTargetLocal {
		t.Fatalf("execution_target = %q", host.ExecutionTarget)
	}
	if !host.Shell {
		t.Fatal("expected shell=true for sidecar host")
	}
}

func TestBoardHostOverride(t *testing.T) {
	t.Cleanup(func() { platform.SetBoardHostOverride(nil) })
	platform.SetBoardHostOverride(&api.BoardHostSlice{
		OS:              "linux",
		Arch:            "amd64",
		ExecutionTarget: api.ExecutionTargetRunner,
		Shell:           false,
	})
	host := platform.BoardHost(api.ExecutionTargetLocal)
	if host.OS != "linux" || host.Arch != "amd64" {
		t.Fatalf("override not applied: %+v", host)
	}
	if host.ExecutionTarget != api.ExecutionTargetRunner {
		t.Fatalf("execution_target = %q", host.ExecutionTarget)
	}
	if host.Shell {
		t.Fatal("expected shell=false from override")
	}
}
