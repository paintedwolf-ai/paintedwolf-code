package wiring

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gitcredential"
	"github.com/lycaon/lycaon/internal/scan/scanworker"
)

// Main dispatches re-executed helper verbs before starting the test suite.
func Main(m *testing.M) int {
	switch {
	case len(os.Args) == 3 && os.Args[1] == "git-credential-vault":
		if err := gitcredential.Forward(os.Args[2], os.Getenv(gitcredential.SocketEnv), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "credential helper:", err)
			return 1
		}
		return 0
	case len(os.Args) > 1 && os.Args[1] == SSHAgentProbeVerb:
		return probeSSHAgent()
	case slices.Contains(os.Args[1:], "internal-scan-worker"):
		if err := scanworker.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
			return 1
		}
		return 0
	}
	return runHostTests(m)
}

// SSHAgentProbeVerb selects the fixture's SSH agent probe.
const SSHAgentProbeVerb = "pw-test-ssh-agent-probe"

// SSHAgentAnswered reports that the agent returned a valid reply.
const SSHAgentAnswered = "ssh agent answered"

func probeSSHAgent() int {
	conn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ssh agent:", err)
		return 255
	}
	defer func() { _ = conn.Close() }()
	reply := make([]byte, 5)
	_, err = conn.Write([]byte{0, 0, 0, 1, 11})
	if err == nil {
		_, err = io.ReadFull(conn, reply)
	}
	if err != nil || reply[4] != 12 {
		fmt.Fprintln(os.Stderr, "ssh agent:", err)
		return 255
	}
	fmt.Fprintln(os.Stderr, SSHAgentAnswered)
	return 255
}
