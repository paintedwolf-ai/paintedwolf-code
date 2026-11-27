package gitexec

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/sshproxy"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "ssh-proxy-command" {
		if err := sshproxy.RunProxyCommand(context.Background(), os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, "ssh-proxy-command:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	gittestsetup.Enable()
	os.Exit(m.Run())
}
