package projectpaths_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProjectPathBoundariesRefuseUnsafeAttachedRoot(t *testing.T) {
	tctx := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{
			ID: "root", Label: "root", Path: string(filepath.Separator), IsPrimary: true,
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
			if reject.Data["reason"] != confine.WriteRootCodeFilesystemRoot {
				t.Fatalf("reject data = %+v, want reason %q", reject.Data, confine.WriteRootCodeFilesystemRoot)
			}
		})
	}
}
