package projectpaths_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProjectPathBoundariesRefuseUnsafeAttachedRoot(t *testing.T) {
	for _, root := range refusedProjectRoots(t) {
		t.Run(root.name, func(t *testing.T) {
			tctx := tools.ToolContext{
				Source: tools.InvocationSource{Roots: []projectroot.RootRef{{
					ID: "root", Label: "root", Path: root.path, IsPrimary: true,
				}},
					ActiveRootID: "root"},
			}
			checks := map[string]func() error{
				"read": func() error {
					_, err := projectpaths.ResolveRead(context.Background(), nil, tctx, "tmp")
					return err
				},
				"write": func() error {
					_, err := projectpaths.ResolveWrite(context.Background(), nil, tctx, "tmp/file")
					return err
				},
				"union": func() error {
					_, err := projectpaths.UnionDiscoveryRoots(context.Background(), tctx, ".")
					return err
				},
				"command cwd": func() error {
					_, _, err := projectpaths.CommandCwd(context.Background(), tctx, "")
					return err
				},
			}
			for name, check := range checks {
				t.Run(name, func(t *testing.T) {
					err := check()
					var reject *toolrejection.ToolReject
					if !errors.As(err, &reject) || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
						t.Fatalf("error = %v, want SANDBOX_CAPABILITY_REQUEST_INVALID ToolReject", err)
					}
					if reject.Data["reason"] != root.code || reject.Data["path"] != filepath.ToSlash(root.path) {
						t.Fatalf("reject data = %+v, want reason %q", reject.Data, root.code)
					}
				})
			}
		})
	}
}

func refusedProjectRoots(t *testing.T) []struct{ name, path, code string } {
	t.Helper()
	store := t.TempDir()
	previous := confine.KeyMaterialWritePaths()
	confine.SetKeyMaterialPathsSource(func() []string { return []string{store} })
	t.Cleanup(func() { confine.SetKeyMaterialPathsSource(func() []string { return previous }) })
	alias := filepath.Join(t.TempDir(), "key-alias")
	testutil.FailErr(t, "create key alias", os.Symlink(store, alias))
	return []struct{ name, path, code string }{
		{"key-store", store, confine.WriteRootCodeSecretStore},
		{"key-alias", alias, confine.WriteRootCodeSecretStore},
		{"relative", "relative-project", confine.WriteRootCodeNotAbsolute},
	}
}
