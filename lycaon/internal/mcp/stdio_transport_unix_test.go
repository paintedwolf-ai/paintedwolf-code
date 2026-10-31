//go:build unix

package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStdioSessionCloseTerminatesSurvivingSessionDescendants(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	root := t.TempDir()
	bin := buildFakeStdioServer(t)
	pidFile := filepath.Join(root, "descendant.pid")
	wrapper := filepath.Join(root, "wrapper.sh")
	script := fmt.Sprintf(
		"#!/bin/sh\nset -m\nsleep 30 </dev/null >/dev/null 2>&1 & echo $! > %s\nexec %s \"$@\"\n",
		strconv.Quote(pidFile), strconv.Quote(bin),
	)
	testutil.FailErr(t, "write wrapper", os.WriteFile(wrapper, []byte(script), 0o755))

	conn := SDKConnector{}
	sess, err := conn.Connect(context.Background(), MCPProviderEntry{
		ID:      "fixture",
		Command: wrapper,
		Enabled: true,
	}, ConnectOpts{Roots: []string{root}})
	testutil.FailErr(t, "connect", err)

	var descendant int
	testutil.WaitFor(t, func() bool {
		raw, readErr := os.ReadFile(pidFile)
		if readErr != nil {
			return false
		}
		pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if convErr != nil || pid <= 0 {
			return false
		}
		descendant = pid
		return true
	})
	t.Cleanup(func() { _ = syscall.Kill(descendant, syscall.SIGKILL) })

	testutil.FailErr(t, "close session", sess.Close())
	testutil.WaitFor(t, func() bool {
		return !osprocess.Alive(descendant)
	})
}
