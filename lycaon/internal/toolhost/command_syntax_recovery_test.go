//go:build darwin || linux

package toolhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestPortableSyntaxOverrideDoesNotEscapeItsOperation(t *testing.T) {
	fixture, _, _, _ := composedMutationEntry(t)
	body, err := fixture.Call(t, t.Context(), `printf 'package p\nfunc (' | tee --syntax-override-reason 'intentional intermediate source' accepted.go; printf 'package p\nfunc (' > rejected.go`)
	testutil.FailErr(t, "run portable mutation recovery", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(body), &outcome))
	if outcome.Refusal == nil || outcome.Refusal.Code != "MUTATION_BROKE_PARSE" || outcome.CompletedEffects != 1 || outcome.UncertainEffects != 0 {
		t.Fatalf("program lost operation scoping or completed effects: %+v", outcome)
	}
	accepted, err := os.ReadFile(filepath.Join(fixture.Dir, "accepted.go"))
	if err != nil || string(accepted) != "package p\nfunc (" {
		t.Fatalf("approved syntax bytes = %q, %v", accepted, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.Dir, "rejected.go")); !os.IsNotExist(err) {
		t.Fatalf("override leaked into redirect: %v", err)
	}
}
