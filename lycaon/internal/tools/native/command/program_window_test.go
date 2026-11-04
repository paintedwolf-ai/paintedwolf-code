package command_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const writeGen = `external -- /bin/sh -c 'echo generated > gen.txt'`

// sessionEffects reads the session's recorded effects by path.
func sessionEffects(t *testing.T, f *programtest.Fixture, ledger *sourceledger.Store) map[string]sourceledger.Effect {
	t.Helper()
	walk, err := ledger.Walk.QueryWalk(t.Context(), testdbseed.DefaultProjectID, sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: f.Session}, 200, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query session walk", err)
	effects := map[string]sourceledger.Effect{}
	for _, file := range walk.Files {
		for _, effect := range file.Effects {
			effects[effect.Path] = effect
		}
	}
	return effects
}

func TestProgramExternalChangeIsTheSessionsObservedCommand(t *testing.T) {
	f := programtest.New(t, programtest.Options{})
	ledger := f.AttachLedger(t)
	out, err := f.Call(t, t.Context(), writeGen)
	testutil.FailErr(t, "run external program", err)
	if outcome := decodeOutcome(t, out); outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("program outcome %s", out)
	}
	effect, ok := sessionEffects(t, f, ledger)["gen.txt"]
	if !ok {
		t.Fatalf("no session effect for the generated file: %+v", sessionEffects(t, f, ledger))
	}
	if effect.CommandWindowID == "" || effect.SessionID != f.Session || effect.Cause != sourceledger.CauseCommandWindow || effect.Op != api.SourceChangeOpCreate {
		t.Fatalf("generated file effect = %+v", effect)
	}
}
