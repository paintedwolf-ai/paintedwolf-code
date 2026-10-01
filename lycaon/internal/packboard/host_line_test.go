package packboard_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatHostLineLocalSidecar(t *testing.T) {
	got := packboard.FormatHostLine(&api.BoardHostSlice{
		OS:              "darwin",
		Arch:            "arm64",
		ExecutionTarget: api.ExecutionTargetLocal,
		Shell:           true,
	})
	want := "Host: darwin/arm64 · local sidecar · shell"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFormatHostLineRemoteRunner(t *testing.T) {
	got := packboard.FormatHostLine(&api.BoardHostSlice{
		OS:              "linux",
		Arch:            "amd64",
		ExecutionTarget: api.ExecutionTargetRunner,
		Shell:           true,
	})
	want := "Host: linux/amd64 · remote runner · shell"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFormatHostLineWindows(t *testing.T) {
	got := packboard.FormatHostLine(&api.BoardHostSlice{
		OS:              "windows",
		Arch:            "amd64",
		ExecutionTarget: api.ExecutionTargetLocal,
		Shell:           true,
	})
	want := "Host: windows/amd64 · local sidecar · shell"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFormatHostLineOmitsShellWhenDisabled(t *testing.T) {
	got := packboard.FormatHostLine(&api.BoardHostSlice{
		OS:              "linux",
		Arch:            "arm64",
		ExecutionTarget: api.ExecutionTargetLocal,
		Shell:           false,
	})
	want := "Host: linux/arm64 · local sidecar"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFormatHostLineNil(t *testing.T) {
	if got := packboard.FormatHostLine(nil); got != "" {
		t.Fatalf("got %q want empty", got)
	}
}

func TestFormatToolchainsLine(t *testing.T) {
	if got := packboard.FormatToolchainsLine(nil); got != "" {
		t.Fatalf("nil host got %q want empty", got)
	}
	if got := packboard.FormatToolchainsLine(&api.BoardHostSlice{}); got != "" {
		t.Fatalf("empty toolchains got %q want empty", got)
	}
	got := packboard.FormatToolchainsLine(&api.BoardHostSlice{
		Toolchains: []string{"bun 1.3.11", "node 22.1.0", "go 1.26.6"},
	})
	want := "Toolchains: bun 1.3.11 · node 22.1.0 · go 1.26.6"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
