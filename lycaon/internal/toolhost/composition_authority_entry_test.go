//go:build unix

package toolhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// compositionForms wrap one child command in each way a program can reach it.
var compositionForms = []struct {
	name string
	wrap func(target string) string
}{
	{"direct", func(target string) string { return "cp go.mod " + target }},
	{"eval", func(target string) string { return "eval 'cp go.mod " + target + "'" }},
	{"timeout", func(target string) string { return "timeout 5 cp go.mod " + target }},
	{"nohup", func(target string) string { return "nohup cp go.mod " + target }},
	{"function", func(target string) string { return "copy() { cp go.mod \"$1\"; }; copy " + target }},
	{"subshell", func(target string) string { return "( cp go.mod " + target + " )" }},
	{"substitution", func(target string) string { return "result=$(cp go.mod " + target + "); echo \"$result\"" }},
	{"loop", func(target string) string { return "for _ in 1; do cp go.mod " + target + "; done" }},
	{"xargs", func(target string) string { return "echo " + target + " | xargs cp go.mod" }},
	{"find", func(target string) string { return "find go.mod -exec cp {} " + target + " \\;" }},
}

// A child effect has the same authority however a program reaches it: every
// composition form completes an allowed copy and refuses one into a folder
// that is not attached, leaving it untouched.
func TestCompositionFormsPreserveChildAuthority(t *testing.T) {
	var directCode string
	for _, form := range compositionForms {
		t.Run(form.name, func(t *testing.T) {
			f := newPathsFixture(t)
			outside := t.TempDir()
			denied := filepath.Join(outside, "copied")
			allowed := "copied-" + form.name

			f.stdout(t, form.wrap(allowed))
			if data, err := os.ReadFile(filepath.Join(f.api.Path, allowed)); err != nil || string(data) != "module api\n" {
				t.Fatalf("allowed copy through %s: %q, %v", form.name, data, err)
			}

			out, err := f.Call(t, t.Context(), form.wrap(denied))
			testutil.FailErr(t, "run denied program", err)
			outcome := decodeProgramOutcome(t, out)
			if outcome.Refusal == nil {
				t.Fatalf("%s reached an unattached folder: status %d stderr %q", form.name, outcome.Status, outcome.Stderr)
			}
			if _, err := os.Stat(denied); !os.IsNotExist(err) {
				t.Fatalf("%s wrote into an unattached folder: %v", form.name, err)
			}
			if form.name == "direct" {
				directCode = outcome.Refusal.Code
			} else if directCode != "" && outcome.Refusal.Code != directCode {
				t.Fatalf("%s refused with %s, direct with %s", form.name, outcome.Refusal.Code, directCode)
			}
			if !strings.Contains(outcome.Refusal.Feedback, outcome.Refusal.Code) {
				t.Fatalf("%s refusal feedback %q omits its code", form.name, outcome.Refusal.Feedback)
			}
		})
	}
}
