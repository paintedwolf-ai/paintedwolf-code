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
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
)

// Main is every host test package's TestMain body. The host re-executes the
// test binary as its Git credential helper, its scan worker and the ssh a
// test installs, so the binary serves those first; otherwise it runs the
// package's tests on a prepared host process.
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
	gittestsetup.Enable()
	return runHostTests(m)
}

// SSHAgentProbeVerb runs the test binary as the ssh Git starts: it asks the
// agent SSH_AUTH_SOCK names for identities and reports the answer.
const SSHAgentProbeVerb = "pw-test-ssh-agent-probe"

// SSHAgentAnswered is what the probe prints when the agent answered.
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
