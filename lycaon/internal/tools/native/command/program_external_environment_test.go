package command_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// An external process a program launches sees the reduced agent environment,
// the boundary's entries and the program's exports, never what the engine
// inherited from whatever launched it.
func TestProgramExternalLaunchCarriesOnlyTheAgentEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "host-session-token")
	t.Setenv("GITHUB_TOKEN", "ambient-credential")
	t.Setenv("PW_TEST_HOST_ONLY", "host-only")
	t.Setenv("XDG_CACHE_HOME", "/xdg/cache")
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), "PW_PROGRAM_EXPORT=exported external -- /usr/bin/env")
	testutil.FailErr(t, "run env through the program", err)
	var outcome struct {
		Stdout string
		Status int
	}
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(out), &outcome))
	environment := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(outcome.Stdout, "\n"), "\n") {
		key, value, _ := strings.Cut(line, "=")
		environment[key] = value
	}
	if outcome.Status != 0 || len(environment) < 2 {
		t.Fatalf("env outcome %s", out)
	}
	for _, key := range []string{"CLAUDE_CODE_MESSAGING_TOKEN", "GITHUB_TOKEN", "PW_TEST_HOST_ONLY"} {
		if value, leaked := environment[key]; leaked {
			t.Errorf("engine environment %s=%q reached the external process", key, value)
		}
	}
	for key := range environment {
		if strings.HasPrefix(strings.ToUpper(key), "LYCAON_") && key != "LYCAON_SOCKS_PROXY" && key != "LYCAON_SSH_KNOWN_HOSTS" {
			t.Errorf("engine namespace %s reached the external process", key)
		}
	}
	if environment["PW_PROGRAM_EXPORT"] != "exported" || environment["XDG_CACHE_HOME"] != "/xdg/cache" || environment["PATH"] == "" || environment["HOME"] == "" {
		t.Fatalf("agent environment lost exports or plumbing: %v", environment)
	}

	_, err = f.call(t, t.Context(), "LYCAON_API_TOKEN=forged external -- /usr/bin/env")
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "COMMAND_OPERAND_INVALID" {
		t.Fatalf("host-reserved export was not refused: %v", err)
	}
}
