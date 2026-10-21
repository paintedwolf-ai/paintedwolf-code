//go:build darwin

package projectpaths_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	hostexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestActualLaunchUsesCheckedProgramPathHandles(t *testing.T) {
	tc, root := readSessionContext(t)
	// This fixture explicitly attaches the native executable directory for reads.
	// Path conversion alone provides neither this read scope nor launch authority.
	tc.Source.Roots = append(tc.Source.Roots, projectroot.RootRef{ID: "native-fixture", Label: "native-fixture", Path: "/bin"})
	paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "pkg")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	plan := hostexec.AgentLaunch(hostexec.LaunchAgentCommand, "synthetic checked path launch", &confine.Confinement{Roots: []string{root}, Network: confine.NetworkDeny}).WithPathAuthority(paths)
	cmd, cleanup, err := hostexec.PrepareCommand(ctx, "/bin/echo", []string{"literal argument"}, hostexec.ExecOpts{Launch: plan, Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var output bytes.Buffer
	cmd.Stdout = &output
	if err := hostexec.RunInOwnGroup(cmd); err != nil {
		t.Fatal(err)
	}
	if output.String() != "literal argument\n" {
		t.Fatalf("actual output %q", output.String())
	}
}

func TestLaunchInputsPreserveActualTargetReadRefusal(t *testing.T) {
	tc, root := readSessionContext(t)
	if err := os.Symlink("../a.go", filepath.Join(root, "pkg", "alias")); err != nil {
		t.Fatal(err)
	}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"read": true}, ReadGlobs: []string{"pkg", "pkg/**"}}})
	paths, err := projectpaths.NewProgramPaths(t.Context(), boundary, tc, "pkg")
	if err != nil {
		t.Fatal(err)
	}
	if file, err := paths.OpenExecutable(t.Context(), "alias"); err == nil {
		file.Close()
		t.Fatal("launch input bypassed actual target read authority")
	}
	if file, err := paths.OpenExecutable(t.Context(), "/dev/null"); err == nil {
		file.Close()
		t.Fatal("null device became executable input")
	}
}
