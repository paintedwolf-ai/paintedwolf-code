package tools

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPolicyScopeRetainsTheExplicitlyResolvedRoot(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "nested")
	file := filepath.Join(inner, "file.go")
	tc := ToolContext{Source: InvocationSource{Roots: []projectroot.RootRef{
		{ID: "outer", Label: "outer", Path: outer, IsPrimary: true},
		{ID: "inner", Label: "inner", Path: inner},
	}}}
	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{name: "relative root path", path: "nested/file.go", want: "outer"},
		{name: "qualified root path", path: "@outer/nested/file.go", want: "outer"},
		{name: "qualified other root", path: "@inner/file.go", want: "inner"},
		{name: "unqualified absolute path", path: file, want: "inner"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var observed []agentpolicy.Target
			ctx := WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
				observed = append(observed, targets...)
				return nil
			})
			testutil.FailErr(t, "resolved scope policy", tc.CheckScopePolicy(ctx, "outer", test.path, true))
			if len(observed) != 1 || observed[0].RootID != test.want || observed[0].Path != file || !observed[0].Mutation {
				t.Fatalf("resolved policy targets = %+v", observed)
			}
		})
	}
	called := false
	ctx := WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
		called = true
		if len(targets) != 1 || targets[0].RootID != "outer" {
			t.Fatalf("explicit policy target lost its root binding: %+v", targets)
		}
		return nil
	})
	testutil.FailErr(t, "explicit target root", tc.CheckPolicyTargets(ctx, []agentpolicy.Target{{RootID: "outer", Path: file, Mutation: true}}))
	if !called {
		t.Fatal("explicit target skipped policy admission")
	}
}

func TestExplicitPolicyRootCannotFallBackToAnotherAttachment(t *testing.T) {
	root := t.TempDir()
	tc := ToolContext{Source: InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Label: "root", Path: root, IsPrimary: true}}}}
	called := false
	ctx := WithAgentPolicy(t.Context(), func(context.Context, []agentpolicy.Target) error {
		called = true
		return nil
	})
	for _, test := range []struct {
		target agentpolicy.Target
		want   error
	}{
		{target: agentpolicy.Target{RootID: "unknown", Path: filepath.Join(root, "file.go")}, want: projectroot.ErrUnknownRootLabel},
		{target: agentpolicy.Target{RootID: "root", Path: filepath.Join(t.TempDir(), "file.go")}, want: projectroot.ErrPathEscape},
	} {
		if err := tc.CheckPolicyTargets(ctx, []agentpolicy.Target{test.target}); !errors.Is(err, test.want) {
			t.Fatalf("explicit root error = %v, want %v", err, test.want)
		}
	}
	if called {
		t.Fatal("invalid root reached policy admission")
	}
}
