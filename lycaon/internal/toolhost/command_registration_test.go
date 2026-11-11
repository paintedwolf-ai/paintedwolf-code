package toolhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestProcessOperationsAreNotCallableTools(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	var cmd *command.CommandTool
	var verify *native.VerifyTool
	var output *command.CommandOutputTool
	var stop *command.CommandStopTool
	testutil.FailErr(t, "register command tools", registerCommandTools(reg, nil, sandbox.NewBoundary(sandbox.Config{}, nil), &cmd, &verify, &output, &stop))
	for _, name := range []string{"process_list", "process_signal"} {
		if _, found := reg.Definition(name); found {
			t.Fatalf("command operation %s is callable", name)
		}
	}
}

func TestCommandRegistrationRefusesUndeclaredToolAtCatalogBoundary(t *testing.T) {
	names := []string{"command", "verify", "command_output", "command_stop"}
	for missing, name := range names {
		t.Run(name, func(t *testing.T) {
			entries := map[string]toolschema.Entry{}
			for i := 0; i < missing; i++ {
				entries[names[i]] = toolschema.Entry{Description: "fixture", Schema: map[string]any{"type": "object"}}
			}
			reg, err := tools.NewCatalogRegistry(&toolschema.Config{Tools: entries})
			if err != nil {
				testutil.FailErr(t, "build restricted metadata registry", err)
			}
			var cmd *command.CommandTool
			var verify *native.VerifyTool
			var output *command.CommandOutputTool
			var stop *command.CommandStopTool
			if err := registerCommandTools(reg, nil, sandbox.NewBoundary(sandbox.Config{}, nil), &cmd, &verify, &output, &stop); err == nil {
				t.Fatal("undeclared command tool registered")
			}
			if len(reg.List()) != missing {
				t.Fatalf("registered tools=%d want=%d", len(reg.List()), missing)
			}
			for _, meta := range reg.List() {
				if meta.Name == name {
					t.Fatal("missing metadata tool became callable")
				}
			}
		})
	}
}

func TestComposedProgramOwnersRunNativeProcessSubstitutions(t *testing.T) {
	f := newComposedProgramFixture(t)
	testutil.FailErr(t, "seed a.txt", os.WriteFile(filepath.Join(f.Dir, "a.txt"), []byte("beta\nalpha\ngamma\n"), 0o600))
	testutil.FailErr(t, "seed b.txt", os.WriteFile(filepath.Join(f.Dir, "b.txt"), []byte("beta\nalpha\ndelta\n"), 0o600))

	raw, err := f.Call(t, t.Context(), `
comm -12 <(sort a.txt) <(sort b.txt)
paste <(printf 'k1\nk2\n') <(printf 'v1\nv2\n')
diff -u <(sort a.txt) <(sort b.txt) || printf 'diff_status=%d\n' "$?"
printf 'hello\n' > >(tr 'a-z' 'A-Z')
`)
	testutil.FailErr(t, "run composed process substitutions", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(raw), &outcome))
	if outcome.Status != 0 || outcome.State != "completed" {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	for _, want := range []string{
		"alpha\nbeta\n",
		"k1\tv1\nk2\tv2\n",
		"-gamma\n+delta\n",
		"diff_status=1\n",
		"HELLO\n",
	} {
		if !strings.Contains(outcome.Stdout, want) {
			t.Fatalf("stdout %q missing %q", outcome.Stdout, want)
		}
	}
}
