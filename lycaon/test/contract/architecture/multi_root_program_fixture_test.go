package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// Each retired operation is exercised through its declared native command form.
// The table is test input, never a compatibility dispatcher or registered tool.
func multiRootCommandProbe(operation, path string) (string, bool) {
	path = "\"$(native_path -- '" + strings.ReplaceAll(path, "'", "'\\''") + "')\""
	probes := map[string]string{
		"read": "cat -- " + path, "write": "printf probe > " + path,
		"grep": "rg probe " + path, "find": "find " + path + " -name '*'",
		"list_dir": "ls -- " + path, "stat": "stat -- " + path, "wc": "wc -c -- " + path,
		"jq": "jq '.' " + path, "copy": "cp -- " + path + " copy-probe", "move": "mv -- " + path + " move-probe",
		"delete": "rm -- " + path, "mkdir": "mkdir -- " + path, "diff": "diff -- " + path + " " + path,
		"git_blame": "git blame -- " + path,
	}
	source, ok := probes[operation]
	return source, ok
}

func rootProgram(t *testing.T, source string, tc tools.ToolContext) command.ProgramOutcome {
	t.Helper()
	out, err := toolfixture.NewProgramFixture(t).Invoke(t, source, tc)
	contractcheck.FailErr(t, "invoke scoped command program", err)
	return out
}

func assertRootProgramSuccess(t *testing.T, out command.ProgramOutcome) string {
	t.Helper()
	if out.Status != 0 || out.Refusal != nil {
		t.Fatalf("scoped command failed: %+v", out)
	}
	return out.Stdout
}
