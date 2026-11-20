package platform_test

import (
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBoardHostUsesRuntimePlatform(t *testing.T) {
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

func TestBoardHostExecutionTarget(t *testing.T) {
	for _, target := range []api.ExecutionTarget{api.ExecutionTargetLocal, api.ExecutionTargetRunner, ""} {
		want := target
		if want == "" {
			want = api.ExecutionTargetLocal
		}
		if got := platform.BoardHost(target).ExecutionTarget; got != want {
			t.Errorf("execution target %q produced %q, want %q", target, got, want)
		}
	}
}
