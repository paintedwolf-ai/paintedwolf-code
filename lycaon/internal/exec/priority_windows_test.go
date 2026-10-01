//go:build windows

package exec

import (
	"context"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPriorityBelowNormalCreationFlags(t *testing.T) {
	guard, err := newRunGuard(ProcessPriorityBelowNormal)
	testutil.FailErr(t, "newRunGuard failed", err)
	defer guard.release()

	cmd := exec.CommandContext(context.Background(), "cmd", "/c", "exit", "0")
	guard.configure(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("expected SysProcAttr")
	}
	flags := cmd.SysProcAttr.CreationFlags
	if flags&windows.BELOW_NORMAL_PRIORITY_CLASS == 0 {
		t.Fatalf("CreationFlags = %#x missing BELOW_NORMAL_PRIORITY_CLASS", flags)
	}
	if flags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("CreationFlags = %#x missing CREATE_NEW_PROCESS_GROUP", flags)
	}
}

func TestPriorityNormalOmitsBelowNormal(t *testing.T) {
	guard, err := newRunGuard(ProcessPriorityNormal)
	testutil.FailErr(t, "newRunGuard failed", err)
	defer guard.release()

	cmd := exec.CommandContext(context.Background(), "cmd", "/c", "exit", "0")
	guard.configure(cmd)
	if cmd.SysProcAttr.CreationFlags&windows.BELOW_NORMAL_PRIORITY_CLASS != 0 {
		t.Fatalf("unexpected BELOW_NORMAL in %#x", cmd.SysProcAttr.CreationFlags)
	}
}
