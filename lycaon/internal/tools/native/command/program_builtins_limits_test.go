package command

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestSessionOutputStateBoundaryReleasesForLaterPrograms(t *testing.T) {
	resources := newSessionOutputResources()
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "toolcontract.LanguageV1 failed", err)
	var maximum int64
	for _, limit := range catalog.Language.Limits {
		if limit.ID == "session_output_state_bytes" {
			maximum = limit.Maximum
			break
		}
	}
	if maximum == 0 {
		t.Fatal("missing session output state limit")
	}
	release, err := resources.ReserveProgramState(t.Context(), maximum)
	testutil.FailErr(t, "resources.ReserveProgramState failed", err)
	_, err = resources.ReserveProgramState(t.Context(), 1)
	var limit *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &limit) || limit.Account != "session_output_state" || limit.Maximum != maximum || limit.Observed != maximum+1 {
		t.Fatalf("one-past session state limit: %v", err)
	}
	release()
	if used := resources.InUse(); used.State != 0 {
		t.Fatalf("released session state in use: %+v", used)
	}
	fresh, err := resources.ReserveProgramState(t.Context(), 1)
	if err != nil {
		t.Fatalf("later program could not reserve: %v", err)
	}
	fresh()
}
