//go:build darwin || linux

package projectpaths_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// outsideGrantFixture lays out an installed file reached through a link in a
// separate directory, as package managers install executables.
func outsideGrantFixture(t *testing.T) (target, link, other string) {
	t.Helper()
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "locate home", err)
	if hostHome := os.Getenv("PW_TEST_HOST_HOME"); hostHome != "" {
		home = hostHome
	}
	//nolint:usetesting // The fixture must be outside project and system temporary roots.
	outside, err := os.MkdirTemp(home, "paintedwolf-leaf-link-*")
	testutil.FailErr(t, "create external fixture", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	for _, dir := range []string{"cellar/tool/1.0/bin", "bin", "other"} {
		testutil.FailErr(t, "create "+dir, os.MkdirAll(filepath.Join(outside, dir), 0o755))
	}
	target = filepath.Join(outside, "cellar/tool/1.0/bin/tool")
	other = filepath.Join(outside, "other/tool")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("approved"), 0o600))
	testutil.FailErr(t, "seed other", os.WriteFile(other, []byte("not approved"), 0o600))
	link = filepath.Join(outside, "bin/tool")
	testutil.FailErr(t, "link target", os.Symlink("../cellar/tool/1.0/bin/tool", link))
	return target, link, other
}

func outsideGrantContext(t *testing.T) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "chat"},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}, ActiveRootID: "root"},
	}
}

func readBound(t *testing.T, paths *projectpaths.ProgramPaths, operand string) (string, error) {
	t.Helper()
	bound, err := paths.Bind(t.Context(), operand, false)
	if err != nil {
		return "", err
	}
	file, err := bound.Open()
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	return string(data), err
}

// An approved read reached through a leaf symlink opens the approved object,
// and only while the link still resolves to it.
func TestOutsideGrantOpensItsObjectThroughALeafSymlink(t *testing.T) {
	for _, shape := range []string{"folder", "current-action"} {
		t.Run(shape, func(t *testing.T) {
			target, link, other := outsideGrantFixture(t)
			tc := outsideGrantContext(t)
			switch shape {
			case "folder":
				runtime := grantedpath.NewRuntime()
				_, err := runtime.Grant("chat", grantedpath.Grant{ID: "grant_folder", Path: filepath.Dir(grantedpath.Normalize(target)), Mode: grantedpath.ModeRead, Tree: true})
				testutil.FailErr(t, "install folder grant", err)
				t.Cleanup(projectpaths.SetGrantedAccessSource(func(session, project, path string, write bool) (projectpaths.Access, bool) {
					g, ok := runtime.Covers(session, project, path, grantedpath.ModeRead)
					return projectpaths.Access{Path: g.Path, Tree: g.Tree}, ok && !write
				}))
			case "current-action":
				tc.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: grantedpath.Normalize(link)}}
			}
			paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, ".")
			testutil.FailErr(t, "bind cwd", err)
			for _, operand := range []string{link, target} {
				data, err := readBound(t, paths, operand)
				testutil.FailErr(t, "read "+operand, err)
				if data != "approved" {
					t.Fatalf("%s read %q", operand, data)
				}
			}
			testutil.FailErr(t, "unlink", os.Remove(link))
			testutil.FailErr(t, "retarget", os.Symlink(other, link))
			if data, err := readBound(t, paths, link); err == nil {
				t.Fatalf("retargeted link opened %q under the old grant", data)
			}
		})
	}
}
