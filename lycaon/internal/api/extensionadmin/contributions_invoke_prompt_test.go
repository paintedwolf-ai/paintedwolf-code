package extensionadmin

import (
	"os"
	"path/filepath"
	"strings"
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
			out, err := renderEditorPrompt(t.Context(), []byte(body), ctx)
			if err == nil {
				t.Fatalf("%s rendered instead of being refused: %q", name, out)
			}
			if strings.Contains(out, secret) {
				t.Fatalf("%s leaked the target file into the prompt", name)
			}
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
	)
	testutil.FailErr(t, "render", err)
	if out != "Rewrite src/a.go lines 4-9 around Parse." {
		t.Fatalf("unexpected render: %q", out)
	}
}
