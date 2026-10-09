package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCredentialRecognitionReloadFeedsOAR(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	base, err := secretmint.LoadBundled()
	testutil.FailErr(t, "load baseline", err)
	unit, err := secretmint.ParseContribution([]byte("version: 1\nenv_keys: [ACCESS_VALUE]"))
	testutil.FailErr(t, "parse extension", err)
	extended, err := secretmint.Compile([]secretmint.Contribution{unit})
	testutil.FailErr(t, "compile extension", err)
	active := base
	calls := 0
	mgr.ToolPolicy.SetCredentialSlotProvider(func(context.Context, *api.Session) *secretmint.Inspector {
		calls++
		return active
	})
	for _, step := range []struct {
		name string
		ins  *secretmint.Inspector
		want bool
	}{
		{"before install", base, false},
		{"enabled", extended, true},
		{"removed", base, false},
	} {
		active = step.ins
		output, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: step.name}, "external_tool", map[string]any{"ACCESS_VALUE": "password"}, "completed", 0, guidance.ToolResultFacts{}.WithOutcome(api.ToolResultOutcomeCompleted))
		if facts.HasCode("WEAK_CREDENTIAL_LITERAL") != step.want || !facts.Succeeded() {
			t.Fatalf("%s: unexpected OAR feedback: %#v", step.name, facts)
		}
		if strings.Contains(output, "ACCESS_VALUE=password") {
			t.Fatal("credential value escaped into advisory")
		}
	}
	if calls != 3 {
		t.Fatalf("provider calls = %d; each invocation must select current recognition", calls)
	}
}
