//go:build unix

package toolhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// M09 owner-fixture evidence: exercises toolhost reopen against the same SQLite
// store.db with explicit recovery and receipt reads. Full isolated sidecar
// restart and normal reconnect are verified separately on the integrated candidate.

// M09 (owner-fixture evidence): Precommit abandon followed by store.db reopen.
func TestBB05_M09_RestartAfterPrecommitFailurePreservesBaselineAndRunsFreshCommand(t *testing.T) {
	ctx := t.Context()
	env := newBB05RestartEnv(t, "bb05-m09-precommit")
	target := filepath.Join(env.projectDir, "target.txt")
	testutil.FailErr(t, "write baseline target", os.WriteFile(target, []byte("baseline\n"), 0o600))

	host1 := env.boot(t, false)
	raw, err := host1.call(t, ctx, "(printf 'mutated\\n'; false) > target.txt")
	testutil.FailErr(t, "run precommit-abandoned program", err)
	outcome := decodeProgramOutcome(t, raw)
	if outcome.Status == 0 || outcome.CompletedEffects != 0 {
		t.Fatalf("precommit failure outcome = %+v, want nonzero status and 0 completed effects", outcome)
	}
	body, err := os.ReadFile(target)
	testutil.FailErr(t, "read target after precommit abandon", err)
	if string(body) != "baseline\n" || host1.programCount(t) != 1 {
		t.Fatalf("precommit failure modified target=%q count=%d", body, host1.programCount(t))
	}

	host1.close(t)
	host2 := env.boot(t, true)
	reloaded, err := host2.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, outcome.ProgramID)
	testutil.FailErr(t, "fresh read after store.db reopen", err)
	var recOutcome command.ProgramOutcome
	testutil.FailErr(t, "decode reloaded outcome", json.Unmarshal([]byte(reloaded), &recOutcome))
	if recOutcome.ProgramID != outcome.ProgramID || recOutcome.Status == 0 || recOutcome.CompletedEffects != 0 || host2.programCount(t) != 1 {
		t.Fatalf("reloaded precommit outcome = %+v count=%d", recOutcome, host2.programCount(t))
	}

	nextRaw, err := host2.call(t, ctx, "printf 'after-restart\\n' > target.txt")
	testutil.FailErr(t, "run new command after reopen", err)
	nextOutcome := decodeProgramOutcome(t, nextRaw)
	body, err = os.ReadFile(target)
	testutil.FailErr(t, "read updated target", err)
	if nextOutcome.State != "completed" || nextOutcome.Status != 0 || string(body) != "after-restart\n" {
		t.Fatalf("post-reopen command outcome=%+v body=%q", nextOutcome, body)
	}
}

// M09 (owner-fixture evidence): Committed effect before result delivery, then store.db reopen + recovery.
func TestBB05_M09_RestartAfterCommittedEffectBeforeResultDelivery(t *testing.T) {
	ctx := t.Context()
	env := newBB05RestartEnv(t, "bb05-m09-delivery")
	target := filepath.Join(env.projectDir, "committed.txt")
	testutil.FailErr(t, "write initial target", os.WriteFile(target, []byte("initial\n"), 0o600))

	host1 := env.boot(t, false)
	_, err := host1.database.ExecContext(ctx, `CREATE TRIGGER bb05_abort_delivery BEFORE UPDATE ON invocation_program_publications
		WHEN NEW.state = 'committed'
		BEGIN SELECT RAISE(ABORT, 'delivery interrupted after commit'); END`)
	testutil.FailErr(t, "install delivery failure trigger", err)

	res, err := host1.call(t, ctx, "printf 'committed-bytes\\n' > committed.txt && cat committed.txt")
	if err == nil || res != "" {
		t.Fatalf("expected delivery failure, got res=%q err=%v", res, err)
	}
	body, err := os.ReadFile(target)
	testutil.FailErr(t, "read committed target", err)
	if string(body) != "committed-bytes\n" {
		t.Fatalf("committed bytes lost: %q", body)
	}

	var programID string
	err = host1.database.QueryRowContext(ctx, `SELECT id FROM invocation_programs`).Scan(&programID)
	testutil.FailErr(t, "query program id", err)
	if pendingRes, pendingErr := host1.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, programID); pendingErr == nil || pendingRes != "" {
		t.Fatalf("pending publication reported completed result: %q / %v", pendingRes, pendingErr)
	}

	_, err = host1.database.ExecContext(ctx, `DROP TRIGGER bb05_abort_delivery`)
	testutil.FailErr(t, "drop delivery failure trigger", err)
	host1.close(t)

	host2 := env.boot(t, true)
	resolveOutputDir := func(_ context.Context, sessionID string) (string, error) {
		if sessionID != env.sessionID {
			return "", fmt.Errorf("unexpected session %q", sessionID)
		}
		return env.outputDir, nil
	}
	testutil.FailErr(t, "recover publications after reopen", host2.runtime.Commands.RecoverProgramPublications(ctx, resolveOutputDir))
	testutil.FailErr(t, "repeat recovery", host2.runtime.Commands.RecoverProgramPublications(ctx, resolveOutputDir))

	body, err = os.ReadFile(target)
	testutil.FailErr(t, "read target after recovery", err)
	if host2.programCount(t) != 1 || string(body) != "committed-bytes\n" {
		t.Fatalf("recovery mutated effect: count=%d body=%q", host2.programCount(t), body)
	}
	reloadedRaw, err := host2.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, programID)
	testutil.FailErr(t, "read recovered receipt", err)
	var reloaded command.ProgramOutcome
	testutil.FailErr(t, "decode recovered receipt", json.Unmarshal([]byte(reloadedRaw), &reloaded))
	if reloaded.ProgramID != programID || reloaded.State != "completed" || len(reloaded.Outputs) != 1 {
		t.Fatalf("recovered receipt = %+v", reloaded)
	}

	freshRaw, err := host2.call(t, ctx, "cat committed.txt")
	testutil.FailErr(t, "run new command on reopened host", err)
	fresh := decodeProgramOutcome(t, freshRaw)
	if fresh.State != "completed" || fresh.Status != 0 || fresh.Stdout != "committed-bytes\n" {
		t.Fatalf("fresh command after reopen = %+v", fresh)
	}
}

// M09 (owner-fixture evidence): Reconnect without restart and reopen after parked work.
func TestBB05_M09_ReconnectWithoutRestartAndRestartAfterParkedWork(t *testing.T) {
	t.Run("reconnect_without_restart", func(t *testing.T) {
		ctx := t.Context()
		env := newBB05RestartEnv(t, "bb05-m09-reconnect")
		host := env.boot(t, false)

		raw, err := host.call(t, ctx, "printf 'once\\n' > log.txt")
		testutil.FailErr(t, "run initial write", err)
		first := decodeProgramOutcome(t, raw)
		if first.State != "completed" || first.Status != 0 {
			t.Fatalf("initial write = %+v", first)
		}
		for i := range 3 {
			receiptRaw, err := host.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, first.ProgramID)
			testutil.FailErr(t, fmt.Sprintf("reconnect read %d", i), err)
			var receipt command.ProgramOutcome
			testutil.FailErr(t, "decode reconnect receipt", json.Unmarshal([]byte(receiptRaw), &receipt))
			if receipt.ProgramID != first.ProgramID || receipt.State != "completed" || receipt.Status != 0 {
				t.Fatalf("reconnect receipt %d = %+v", i, receipt)
			}
		}
		body, err := os.ReadFile(filepath.Join(env.projectDir, "log.txt"))
		testutil.FailErr(t, "read log.txt after reconnect", err)
		if host.programCount(t) != 1 || string(body) != "once\n" {
			t.Fatalf("reconnect replayed effect: count=%d body=%q", host.programCount(t), body)
		}
		nextRaw, err := host.call(t, ctx, "cat log.txt")
		testutil.FailErr(t, "run follow-up command", err)
		if next := decodeProgramOutcome(t, nextRaw); next.State != "completed" || next.Stdout != "once\n" {
			t.Fatalf("follow-up after reconnect = %+v", next)
		}
	})

	t.Run("restart_after_parked_work", func(t *testing.T) {
		ctx := t.Context()
		env := newBB05RestartEnv(t, "bb05-m09-parked")
		host1 := env.boot(t, false)

		clock := programtest.NewManualClock()
		waiting := programtest.NewWaitingProgram(clock, programtest.SleepState(600*time.Second))
		parkedCh := make(chan *waitStatePark, 1)
		host1.runtime.Commands.SetProgramParking(waitStateParking{parked: parkedCh})
		host1.runtime.Commands.programs().Clock = clock
		host1.runtime.Commands.programs().Evaluate = waiting.Evaluate

		raw, err := host1.call(t, ctx, "sleep 600")
		testutil.FailErr(t, "park long wait", err)
		var parked command.ParkedProgramResult
		testutil.FailErr(t, "decode parked result", json.Unmarshal([]byte(raw), &parked))
		if parked.State != "parked" || parked.ProgramID == "" {
			t.Fatalf("expected parked program, got %+v", parked)
		}
		recPark := <-parkedCh
		waiting.EndWait()
		<-recPark.settled
		host1.close(t)

		host2 := env.boot(t, true)
		_, err = host2.database.ExecContext(ctx, `UPDATE invocation_programs SET state='running', status_code=NULL, stop_code='', settled_at=NULL WHERE id=?`, parked.ProgramID)
		testutil.FailErr(t, "pin running state across reopen", err)
		interrupted, err := host2.recorder.InterruptRunning(ctx)
		testutil.FailErr(t, "reconcile running programs on reopen", err)
		if interrupted < 1 {
			t.Fatalf("expected >=1 interrupted receipt on reopen, got %d", interrupted)
		}
		lostRaw, err := host2.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, parked.ProgramID)
		testutil.FailErr(t, "read lost parked program", err)
		var lost command.ProgramOutcome
		testutil.FailErr(t, "decode lost outcome", json.Unmarshal([]byte(lostRaw), &lost))
		if lost.ProgramID != parked.ProgramID || lost.State != "interrupted" || lost.Status != 130 || lost.StopCode != toolrejection.ToolOwnerInterruptedCode || host2.programCount(t) != 1 {
			t.Fatalf("lost parked program = %+v count=%d", lost, host2.programCount(t))
		}
		freshRaw, err := host2.call(t, ctx, "echo recovered-after-park")
		testutil.FailErr(t, "run new command after parked reopen", err)
		if fresh := decodeProgramOutcome(t, freshRaw); fresh.State != "completed" || fresh.Status != 0 || fresh.Stdout != "recovered-after-park\n" {
			t.Fatalf("new command after parked reopen = %+v", fresh)
		}
	})
}
