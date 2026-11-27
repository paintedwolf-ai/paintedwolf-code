//go:build unix

package toolhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// I15 (pre-commit side of an effect commit + store reopen):
// A producer failing before commit leaves destination bytes intact and records
// zero completed effects; reopening store.db preserves receipt identity and
// zero effects without replaying, and a subsequent write succeeds.
func TestI15_PreCommitFaultAndStoreReopenPreservesZeroEffects(t *testing.T) {
	ctx := t.Context()
	env := newI15Env(t, "i15-precommit")
	target := filepath.Join(env.projectDir, "target.txt")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("baseline\n"), 0o600))

	host1 := env.boot(t, false)
	raw, err := host1.call(t, ctx, "(printf 'mutated\\n'; false) > target.txt")
	testutil.FailErr(t, "run precommit-abandoned program", err)
	outcome := decodeProgramOutcome(t, raw)
	if outcome.Status == 0 || outcome.CompletedEffects != 0 {
		t.Fatalf("precommit outcome = %+v, want nonzero status and 0 completed effects", outcome)
	}
	body, err := os.ReadFile(target)
	testutil.FailErr(t, "read target", err)
	if string(body) != "baseline\n" || host1.programCount(t) != 1 {
		t.Fatalf("precommit modified target=%q count=%d", body, host1.programCount(t))
	}

	host1.close(t)
	host2 := env.boot(t, true)
	reloadedRaw, err := host2.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, outcome.ProgramID)
	testutil.FailErr(t, "read receipt after reopen", err)
	var reloaded command.ProgramOutcome
	testutil.FailErr(t, "decode reloaded receipt", json.Unmarshal([]byte(reloadedRaw), &reloaded))
	if reloaded.ProgramID != outcome.ProgramID || reloaded.Status == 0 || reloaded.CompletedEffects != 0 || host2.programCount(t) != 1 {
		t.Fatalf("reloaded precommit receipt = %+v count=%d", reloaded, host2.programCount(t))
	}

	nextRaw, err := host2.call(t, ctx, "printf 'after-reopen\\n' > target.txt")
	testutil.FailErr(t, "run fresh write after reopen", err)
	next := decodeProgramOutcome(t, nextRaw)
	body, err = os.ReadFile(target)
	testutil.FailErr(t, "read updated target", err)
	if next.State != "completed" || next.Status != 0 || string(body) != "after-reopen\n" {
		t.Fatalf("post-reopen write outcome=%+v body=%q", next, body)
	}
}

// I15 (deterministic fault seam on post-commit / pre-delivery side + repeated recovery):
// Uses both a file effect and a local HTTP POST fixture to count actual effects.
// Halts at the SQLite publication commit seam after both effects commit; verifies
// pending publication stays distinct from completed exit, store reopen + repeated
// RecoverProgramPublications preserves effect cardinality == 1, and reuse succeeds.
func TestI15_PostCommitPreDeliveryFaultSeamAndRepeatedRecoveryCardinality(t *testing.T) {
	ctx := t.Context()
	var httpHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/effect" {
			httpHits.Add(1)
			_, _ = io.WriteString(w, "http-committed\n")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	env := newI15Env(t, "i15-postcommit-fault", i15ServerPort(t, srv.URL))
	target := filepath.Join(env.projectDir, "committed.txt")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("initial\n"), 0o600))

	host1 := env.boot(t, false)
	_, err := host1.database.ExecContext(ctx, `CREATE TRIGGER i15_abort_delivery BEFORE UPDATE ON invocation_program_publications
		WHEN NEW.state = 'committed'
		BEGIN SELECT RAISE(ABORT, 'deterministic post-commit pre-delivery fault'); END`)
	testutil.FailErr(t, "install deterministic fault trigger", err)

	cmd := fmt.Sprintf("curl -fsS -X POST -d 'k=1' '%s/effect' > committed.txt && cat committed.txt", srv.URL)
	res, err := host1.call(t, ctx, cmd)
	if err == nil || res != "" {
		t.Fatalf("expected fault-seam delivery error, got res=%q err=%v", res, err)
	}
	if httpHits.Load() != 1 {
		t.Fatalf("HTTP effect count before recovery = %d, want 1", httpHits.Load())
	}
	body, err := os.ReadFile(target)
	testutil.FailErr(t, "read committed target", err)
	if string(body) != "http-committed\n" {
		t.Fatalf("committed bytes lost: %q", body)
	}

	var programID string
	err = host1.database.QueryRowContext(ctx, `SELECT id FROM invocation_programs`).Scan(&programID)
	testutil.FailErr(t, "query program id", err)
	// Pending publication must remain distinct from a completed result.
	if pendingRaw, pendingErr := host1.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, programID); pendingErr == nil || pendingRaw != "" {
		t.Fatalf("pending publication fabricated completion: %q / %v", pendingRaw, pendingErr)
	}

	_, err = host1.database.ExecContext(ctx, `DROP TRIGGER i15_abort_delivery`)
	testutil.FailErr(t, "drop fault trigger", err)
	host1.close(t)

	host2 := env.boot(t, true)
	resolveOut := func(_ context.Context, sessionID string) (string, error) {
		if sessionID != env.sessionID {
			return "", fmt.Errorf("unexpected session %q", sessionID)
		}
		return env.outputDir, nil
	}
	// Repeated recovery must be idempotent and never trigger a second effect.
	for i := range 3 {
		testutil.FailErr(t, fmt.Sprintf("recover publication pass %d", i+1), host2.runtime.Commands.RecoverProgramPublications(ctx, resolveOut))
	}
	if httpHits.Load() != 1 || host2.programCount(t) != 1 {
		t.Fatalf("repeated recovery replayed effect: httpHits=%d programs=%d", httpHits.Load(), host2.programCount(t))
	}
	body, err = os.ReadFile(target)
	testutil.FailErr(t, "read target after recovery", err)
	if string(body) != "http-committed\n" {
		t.Fatalf("target changed after recovery: %q", body)
	}

	reloadedRaw, err := host2.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, programID)
	testutil.FailErr(t, "read recovered receipt", err)
	var reloaded command.ProgramOutcome
	testutil.FailErr(t, "decode recovered receipt", json.Unmarshal([]byte(reloadedRaw), &reloaded))
	if reloaded.ProgramID != programID || reloaded.State != "completed" || reloaded.Status != 0 || len(reloaded.Outputs) != 1 {
		t.Fatalf("recovered receipt mismatch: %+v", reloaded)
	}

	// Cross-session receipt read must fail cleanly (scope preserved across reopen).
	if _, crossErr := host2.runtime.Commands.ProgramReceiptResult(ctx, "other-session", programID); crossErr == nil {
		t.Fatal("other session read program receipt across scope boundary")
	}

	freshRaw, err := host2.call(t, ctx, "cat committed.txt")
	testutil.FailErr(t, "run follow-up after recovery", err)
	if fresh := decodeProgramOutcome(t, freshRaw); fresh.State != "completed" || fresh.Status != 0 || fresh.Stdout != "http-committed\n" {
		t.Fatalf("follow-up after recovery = %+v", fresh)
	}
}

// I15 (live reconnect without restart, separate from fault seam):
// Executes an observable file append and local HTTP POST effect, then performs
// repeated live receipt reloads without restarting the host; verifies exact
// cardinality == 1 on both file and HTTP fixtures and clean subsequent reuse.
func TestI15_LiveReconnectWithoutRestartPreservesFileAndHTTPCardinality(t *testing.T) {
	ctx := t.Context()
	var httpHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/once" {
			httpHits.Add(1)
			_, _ = io.WriteString(w, "once-token\n")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	env := newI15Env(t, "i15-live-reconnect", i15ServerPort(t, srv.URL))
	logPath := filepath.Join(env.projectDir, "append.log")
	testutil.FailErr(t, "seed append.log", os.WriteFile(logPath, []byte("init\n"), 0o600))

	host := env.boot(t, false)
	cmd := fmt.Sprintf("curl -fsS -X POST -d 'n=1' '%s/once' >> append.log && cat append.log", srv.URL)
	raw, err := host.call(t, ctx, cmd)
	testutil.FailErr(t, "run live mutating command", err)
	first := decodeProgramOutcome(t, raw)
	if first.State != "completed" || first.Status != 0 || first.ProgramID == "" {
		t.Fatalf("initial run = %+v", first)
	}

	// Simulate repeated live client reconnects reading durable receipts without host restart.
	for i := range 3 {
		recRaw, rErr := host.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, first.ProgramID)
		testutil.FailErr(t, fmt.Sprintf("reconnect read %d", i+1), rErr)
		var rec command.ProgramOutcome
		testutil.FailErr(t, "decode reconnect receipt", json.Unmarshal([]byte(recRaw), &rec))
		if rec.ProgramID != first.ProgramID || rec.State != "completed" || rec.Status != 0 {
			t.Fatalf("reconnect receipt %d = %+v", i+1, rec)
		}
	}

	logBytes, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read append.log", err)
	if httpHits.Load() != 1 || host.programCount(t) != 1 || strings.Count(string(logBytes), "once-token\n") != 1 {
		t.Fatalf("live reconnect replayed effect: httpHits=%d programs=%d log=%q", httpHits.Load(), host.programCount(t), logBytes)
	}

	// Subsequent ordinary command in the same session succeeds without disturbing prior effects.
	nextRaw, err := host.call(t, ctx, "cat append.log")
	testutil.FailErr(t, "run follow-up in same session", err)
	next := decodeProgramOutcome(t, nextRaw)
	if next.State != "completed" || next.Status != 0 || next.Stdout != "init\nonce-token\n" || httpHits.Load() != 1 {
		t.Fatalf("post-reconnect follow-up = %+v httpHits=%d", next, httpHits.Load())
	}
}
