package mcp_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveDistroCommandSelf(t *testing.T) {
	exe, err := os.Executable()
	testutil.FailErr(t, "os.Executable failed", err)
	got, args, err := mcp.ResolveDistroCommand(mcp.DistroCommandSelf, []string{"serve"})
	testutil.FailErr(t, "mcp.ResolveDistroCommand failed", err)
	if got != exe {
		t.Fatalf("command = %q want %q", got, exe)
	}
	if len(args) != 1 || args[0] != "serve" {
		t.Fatalf("args = %v", args)
	}
}

func TestResolveDistroCommandPinned(t *testing.T) {
	got, args, err := mcp.ResolveDistroCommand(filepath.Join("bin", "lycaon"), []string{"mcp"})
	testutil.FailErr(t, "mcp.ResolveDistroCommand failed", err)
	if got != filepath.Join("bin", "lycaon") {
		t.Fatalf("command = %q", got)
	}
	if len(args) != 1 {
		t.Fatalf("args = %v", args)
	}
}
