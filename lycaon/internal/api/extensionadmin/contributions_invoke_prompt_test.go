package extensionadmin

import (
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil/exposure"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// An editor-action prompt body is pack-authored, and reachable by a trusted
// project through the shared kinds. It renders unconfined in the host process
// and its output is admitted as a prompt, so a body that named a file would
// read it with no card and send it to the model.
func TestEditorPromptCannotReadFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "credentials")
	const secret = "SENSITIVE-VALUE-42"
	testutil.FailErr(t, "write target", os.WriteFile(target, []byte(secret), 0o600))

	ctx := wire.CommandInvokeContext{Path: "src/a.go", RootID: "r1"}
	for name, body := range map[string]string{
		"ssi":     `Fix {{ path }}.{% ssi "` + target + `" %}`,
		"include": `Fix {{ path }}.{% include "` + target + `" %}`,
		"extends": `{% extends "` + target + `" %}`,
		"import":  `{% import "` + target + `" as m %}Fix {{ path }}.`,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := renderEditorPrompt(t.Context(), []byte(body), ctx, nil, "")
			if err == nil {
				t.Fatalf("%s rendered instead of being refused: %q", name, out)
			}
			exposure.Absent(t, out, secret)
		})
	}
}

// The structured target still reaches the copy, which is why these are
// templates at all.
func TestEditorPromptRendersStructuredTarget(t *testing.T) {
	out, err := renderEditorPrompt(
		t.Context(),
		[]byte(`Rewrite {{ path }} lines {{ start_line }}-{{ end_line }}{% if symbol %} around {{ symbol }}{% endif %}.`),
		wire.CommandInvokeContext{Path: "src/a.go", StartLine: 4, EndLine: 9, Symbol: "Parse"},
		nil, "",
	)
	testutil.FailErr(t, "render", err)
	if out != "Rewrite src/a.go lines 4-9 around Parse." {
		t.Fatalf("unexpected render: %q", out)
	}
}

// path_display names the selection in the canonical session-folder spelling:
// bare off the session's active root, @label-qualified off any other attached
// root, so an editor prompt matches the attachment fence for the same
// reference (INV-WR-08).
func TestEditorPromptPathDisplayMatchesAttachmentFence(t *testing.T) {
	base := t.TempDir()
	roots := []projectroot.RootRef{
		{ID: "r-app", Label: "app", Path: filepath.Join(base, "app"), IsPrimary: true},
		{ID: "r-lib", Label: "lib", Path: filepath.Join(base, "lib")},
		{ID: "r-bare", Path: filepath.Join(base, "bare")},
	}
	for _, dir := range roots {
		testutil.FailErr(t, "mkdir root", os.MkdirAll(dir.Path, 0o750))
	}
	for _, tc := range []struct {
		name         string
		activeRootID string
		rootID       string
		want         string
	}{
		{name: "session folder", activeRootID: "r-app", rootID: "r-app", want: "src/a.go"},
		{name: "other attached root", activeRootID: "r-app", rootID: "r-lib", want: "@lib/src/a.go"},
		{name: "unlabeled root", activeRootID: "r-app", rootID: "r-bare", want: "src/a.go"},
		{name: "active root follows session", activeRootID: "r-lib", rootID: "r-lib", want: "src/a.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := wire.CommandInvokeContext{Path: "src/a.go", RootID: tc.rootID}
			out, err := renderEditorPrompt(t.Context(), []byte("{{ path_display }}"), ctx, roots, tc.activeRootID)
			testutil.FailErr(t, "render", err)
			if out != tc.want {
				t.Fatalf("path_display = %q, want %q", out, tc.want)
			}
			// The attachment fence resolves the same reference through Qualify;
			// the prompt spelling must never drift from it.
			abs, root, resolveErr := projectroot.ResolveAbs(roots, tc.rootID, ctx.Path)
			testutil.FailErr(t, "resolve target", resolveErr)
			if fenced := projectroot.Qualify(roots, tc.activeRootID, root, abs); fenced != out {
				t.Fatalf("path_display %q disagrees with fence spelling %q", out, fenced)
			}
		})
	}
}

// A target that cannot resolve under the attached roots keeps the raw path
// rather than rendering an empty name.
func TestEditorPromptPathDisplayFallsBackToRawPath(t *testing.T) {
	for name, tc := range map[string]struct {
		roots []projectroot.RootRef
		ctx   wire.CommandInvokeContext
		want  string
	}{
		"no roots":    {roots: nil, ctx: wire.CommandInvokeContext{Path: "src/a.go"}, want: "src/a.go"},
		"path escape": {roots: []projectroot.RootRef{{ID: "r1", Label: "app", Path: t.TempDir()}}, ctx: wire.CommandInvokeContext{Path: "../outside.go", RootID: "r1"}, want: "../outside.go"},
		"empty path":  {roots: []projectroot.RootRef{{ID: "r1", Label: "app", Path: t.TempDir()}}, ctx: wire.CommandInvokeContext{}, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := renderEditorPrompt(t.Context(), []byte("[{{ path_display }}]"), tc.ctx, tc.roots, "")
			testutil.FailErr(t, "render", err)
			if out != "["+tc.want+"]" {
				t.Fatalf("path_display = %q, want %q", out, tc.want)
			}
		})
	}
}
