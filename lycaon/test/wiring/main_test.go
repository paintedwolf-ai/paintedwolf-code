package wiring

import (
	"fmt"
	"io"
	"net"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/gitcredential"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
)

func TestMain(m *testing.M) {
	// Agent Git runs this binary as its credential helper.
	if len(os.Args) == 3 && os.Args[1] == "git-credential-vault" {
		if err := gitcredential.Forward(os.Args[2], os.Getenv(gitcredential.SocketEnv), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "credential helper:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == sshAgentProbeVerb {
		os.Exit(probeSSHAgent())
	}
	gittestsetup.Enable()
	os.Exit(RunReleasingHosts(m))
}

// sshAgentProbeVerb runs this binary as the ssh Git starts: it asks the agent
// SSH_AUTH_SOCK names for identities and reports the answer.
const sshAgentProbeVerb = "pw-test-ssh-agent-probe"

const sshAgentAnswered = "ssh agent answered"

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
	fmt.Fprintln(os.Stderr, sshAgentAnswered)
	return 255
}
