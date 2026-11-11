package wiring

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/gitcredential"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
)

func TestMain(m *testing.M) {
	// Agent Git runs this binary as its credential helper.
	if len(os.Args) == 3 && os.Args[1] == "git-credential-vault" {
		if err := gitcredential.Forward(os.Args[2], os.Getenv(gitcredential.SocketEnv), os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	gittestsetup.Enable()
	os.Exit(RunReleasingHosts(m))
}
