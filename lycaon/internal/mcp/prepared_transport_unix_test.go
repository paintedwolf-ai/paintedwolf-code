//go:build darwin || linux

package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTrackedTransportReturnsBeforeServerInitialization(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// The payload cannot finish until it receives request bytes or SDK-owned EOF.
	// Connect must return without attempting an MCP handshake behind the gate.
	cmd, cleanup, err := execpkg.PrepareCommand(ctx, "/bin/sh", []string{"-c", "read request || exit 0; printf forbidden > effect"}, execpkg.ExecOpts{Launch: execpkg.HostLaunch("synthetic SDK transport ordering"), Dir: root})
	testutil.FailErr(t, "prepare actual native SDK transport", err)
	defer cleanup()
	hardenSpawn(cmd)
	transport := &trackedTransport{CommandTransport: &sdkmcp.CommandTransport{Command: cmd}, cleanup: cleanup}
	connection, err := transport.Connect(ctx)
	testutil.FailErr(t, "start SDK pipes without handshake", err)
	if connection == nil || cmd.Process == nil || cmd.ProcessState != nil {
		t.Fatal("SDK native process lifecycle not retained")
	}
	testutil.FailErr(t, "SDK closes stdin and reaps native process", connection.Close())
	transport.release()
	if cmd.ProcessState == nil {
		t.Fatal("SDK did not settle its own native Wait")
	}
	if _, err := os.Stat(filepath.Join(root, "effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("transport initialization wrote payload input")
	}
}

func TestTrackedTransportSettlesActualPostStartAuthorityRefusal(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fault := errors.New("synthetic current authority revoked")
	checks := 0
	plan := execpkg.HostLaunch("synthetic SDK post-start refusal")
	plan.CurrentAuthority = func(context.Context) error {
		checks++
		if checks > 1 {
			return fault
		}
		return nil
	}
	cmd, cleanup, err := execpkg.PrepareCommand(ctx, "/bin/sh", []string{"-c", "printf forbidden > effect; read request"}, execpkg.ExecOpts{Launch: plan, Dir: root})
	testutil.FailErr(t, "prepare SDK refused handoff", err)
	defer cleanup()
	hardenSpawn(cmd)
	transport := &trackedTransport{CommandTransport: &sdkmcp.CommandTransport{Command: cmd}, cleanup: cleanup}
	connection, err := transport.Connect(ctx)
	if !errors.Is(err, fault) || connection != nil || cmd.ProcessState == nil {
		t.Fatalf("actual SDK post-start failure not settled: %v", err)
	}
	transport.release()
	if _, err := os.Stat(filepath.Join(root, "effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused SDK handoff executed payload")
	}
}
