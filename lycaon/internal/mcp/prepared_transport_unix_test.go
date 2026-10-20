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
