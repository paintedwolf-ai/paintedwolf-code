package projectpaths_test

import (
	"bytes"
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

func TestBoundReadRechecksReusableGrantWithoutRevokingCurrentAction(t *testing.T) {
	for _, currentAction := range []bool{false, true} {
		name := "reusable"
		if currentAction {
			name = "current-action"
		}
		t.Run(name, func(t *testing.T) {
			home, err := os.UserHomeDir()
			testutil.FailErr(t, "locate home", err)
			if hostHome := os.Getenv("PW_TEST_HOST_HOME"); hostHome != "" {
				home = hostHome
			}
			//nolint:usetesting // The fixture must be outside project and system temporary roots.
			outside, err := os.MkdirTemp(home, "paintedwolf-grant-lifetime-*")
			testutil.FailErr(t, "create external fixture", err)
			t.Cleanup(func() { _ = os.RemoveAll(outside) })
			target := filepath.Join(outside, "input")
			const contents = "independently observed source bytes"
			testutil.FailErr(t, "seed external source", os.WriteFile(target, []byte(contents), 0o600))
			runtime := grantedpath.NewRuntime()
			grant := grantedpath.Grant{ID: "grant_read", Path: target, Mode: grantedpath.ModeRead}
			_, err = runtime.Grant("chat", grant)
			testutil.FailErr(t, "install reusable authority", err)
			t.Cleanup(projectpaths.SetGrantedAccessSource(func(session, project, path string, write bool) (projectpaths.Access, bool) {
				mode := grantedpath.ModeRead
				if write {
					mode = grantedpath.ModeWrite
				}
				g, ok := runtime.Covers(session, project, path, mode)
				return projectpaths.Access{Path: g.Path, Tree: g.Tree}, ok
			}))
			tc := tools.ToolContext{
				Identity: tools.InvocationIdentity{SessionID: "chat"},
				Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}, ActiveRootID: "root"},
			}
			if currentAction {
				tc.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: grantedpath.Normalize(target)}}
			}
			paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, ".")
			testutil.FailErr(t, "bind cwd", err)
			bound, err := paths.Bind(t.Context(), target, false)
			testutil.FailErr(t, "bind external source", err)
			read := func() (string, error) {
				file, err := bound.Open()
				if err != nil {
					return "", err
				}
				defer file.Close()
				var output bytes.Buffer
				_, err = io.Copy(&output, file)
				return output.String(), err
			}
			data, err := read()
			testutil.FailErr(t, "read with live authority", err)
			if data != contents {
				t.Fatalf("authorized bytes=%q", data)
			}
			if !runtime.RevokeByID(grant.ID) {
				t.Fatal("fixture grant was not revoked")
			}
			data, err = read()
			if currentAction {
				testutil.FailErr(t, "preserve exact current-action read", err)
				if data != contents {
					t.Fatalf("current-action bytes=%q", data)
				}
				tc.BeginInvocation()
				next, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, ".")
				testutil.FailErr(t, "create next invocation paths", err)
				if _, err := next.Bind(t.Context(), target, false); err == nil {
					t.Fatal("next invocation inherited spent current-action scope")
				}
			} else if err == nil || data != "" {
				t.Fatalf("revoked reusable authority read bytes: err=%v data=%q", err, data)
			}
			_, err = runtime.Grant("chat", grant)
			testutil.FailErr(t, "restore reusable authority", err)
			data, err = read()
			testutil.FailErr(t, "recover bound read after reapproval", err)
			if data != contents {
				t.Fatalf("reapproved bytes=%q", data)
			}
		})
	}
}
