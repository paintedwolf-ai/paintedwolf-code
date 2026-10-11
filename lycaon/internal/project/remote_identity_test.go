package project

import (
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"os/exec"
	"strings"
	"testing"
)

func TestGitRemoteHashIdentifiesTheRepositoryNotItsSpelling(t *testing.T) {
	gittestsetup.Enable()
	hash := func(remote string) string {
		dir := t.TempDir()
		for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
			out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
			testutil.FailErr(t, "git "+strings.Join(args, " ")+": "+string(out), err)
		}
		return gitRemoteHash(t.Context(), dir)
	}
	ssh, https := hash("git@github.com:Painted/Wolf.git"), hash("https://github.com/painted/wolf")
	if ssh == "" || ssh != https {
		t.Fatalf("hashes differ for one repository: %q %q", ssh, https)
	}
	if other := hash("https://github.com/painted/other"); other == ssh {
		t.Fatal("different repositories share a hash")
	}
}
