package contract

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Substitutions are admitted as the argv the external process receives.
func TestApprovedVerificationProgramIsTheExecutedPlan(t *testing.T) {
	f, gate := verificationProgramFixture(t)
	for _, source := range []string{
		`value='two words'; external -- /usr/bin/printf '<%s>\n' "$value"`,
		`external -- /usr/bin/printf '<%s>\n' "$(printf substituted)"`,
	} {
		gate.reset()
		raw, err := f.Invoke(t, t.Context(), "verify", map[string]any{"command": source}, (&native.VerifyTool{Programs: f.Runtime}).Run)
		contractcheck.FailErr(t, "run verification program", err)
		var outcome command.ProgramOutcome
		contractcheck.FailErr(t, "decode verification program", json.Unmarshal([]byte(raw), &outcome))
		approved, ok := gate.reviewed("command")
		if !ok {
			t.Fatal("external launch was not reviewed")
		}
		stages, err := exec.StagesFromCommandLine(approved.Invocation.Args["command"].(string))
		contractcheck.FailErr(t, "reviewed launch", err)
		if len(stages) != 1 || len(stages[0].Args) != 2 || outcome.Stdout != "<"+stages[0].Args[1]+">\n" || outcome.Outcome != "passed" {
			t.Fatalf("reviewed=%+v outcome=%+v", stages, outcome)
		}
	}
}
