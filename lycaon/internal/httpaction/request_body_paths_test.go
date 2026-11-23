package httpaction

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// A request body path and a form file path are session-folder paths: @label
// reads a file in any attached root and an unknown label is refused.
func TestRequestBodyPathsResolveAcrossRoots(t *testing.T) {
	base := t.TempDir()
	primary, secondary := filepath.Join(base, "app"), filepath.Join(base, "lib")
	for _, dir := range []string{primary, secondary} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o755))
	}
	testutil.FailErr(t, "write body", os.WriteFile(filepath.Join(secondary, "payload.txt"), []byte("lib-payload"), 0o600))
	tctx := tools.ToolContext{Source: tools.InvocationSource{
		Roots: []projectroot.RootRef{
			{ID: "p", Label: "app", Path: primary, IsPrimary: true},
			{ID: "s", Label: "lib", Path: secondary},
		},
		ActiveRootID: "p",
	}}
	ctx := context.Background()

	form, err := assembleBody(ctx, nil, tctx, map[string]any{"form": []any{map[string]any{"name": "upload", "path": "@lib/payload.txt"}}})
	testutil.FailErr(t, "assemble labelled form path", err)
	if !bytes.Contains(form.bytes, []byte("lib-payload")) {
		t.Fatalf("form body does not carry the lib root's file: %q", form.bytes)
	}

	_, err = assembleBody(ctx, nil, tctx, map[string]any{"form": []any{map[string]any{"name": "upload", "path": "@missing/payload.txt"}}})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "UNKNOWN_ROOT_LABEL" {
		t.Fatalf("form.path with an unknown label = %v, want UNKNOWN_ROOT_LABEL", err)
	}
}
