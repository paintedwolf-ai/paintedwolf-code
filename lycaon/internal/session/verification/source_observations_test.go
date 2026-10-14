package verification

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func checkRecord(id, revision, root string, at time.Time) evidence.Record {
	return evidence.Record{GateType: string(evidence.GateTypeVerify), Slot: VerifySlot,
		GateVerdict: string(evidence.GateVerdictPassed), RecordedAt: at.Format(time.RFC3339Nano),
		Artifacts: map[string]any{"check_id": id, "is_check": true, "producer": ProducerVerify,
			"command": "test runner", "cwd": ".", "source_revision": revision, "source_root_digest": root}}
}

func TestSourceObservationsTrackExactContentAcrossTime(t *testing.T) {
	ctx := t.Context()
	sessions := store.NewMemory()
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "")
	testutil.FailErr(t, "create session", err)
	sess.ProjectID, sess.WorkspacePath = "project", t.TempDir()
	m := New(sessions, scope.New(sessions), nil)
	m.SetDataDir(t.TempDir())
	m.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	ledger := sourceledger.New(testdbfixture.Open(t, "store.db"), t.TempDir())
	m.SetSourceObservations(ledger.Inventory)
	path := filepath.Join(sess.WorkspacePath, "source.txt")
	write := func(content string) {
		testutil.FailErr(t, "write source", os.WriteFile(path, []byte(content), 0o600))
		repochange.Advance(sess.WorkspacePath)
	}
	write("initial")
	revision, root := m.Revision(ctx, sess.WorkspacePath)
	if revision == "" || root == "" {
		t.Fatal("exact source observation unavailable")
	}
	when := time.Now().UTC()
	record := checkRecord("check", revision, root, when)
	record.RunID = sess.ID
	testutil.FailErr(t, "persist check", m.evidenceStore.Append(ctx, m.EvidenceRootFor(sess), record))
	history := []api.Message{{Role: api.MessageRoleUser, CreatedAt: when.Add(-time.Second)}}
	assert := func(want SourceMatch, prior bool) {
		t.Helper()
		out := m.SourceObservations(ctx, sess, history)
		if out == nil || len(out.Checks) != 1 || out.Checks[0].Match != want || out.Checks[0].PriorIntent != prior || out.Checks[0].Verdict != string(evidence.GateVerdictPassed) || out.RequiredByWorkflow {
			t.Fatalf("source observations=%+v, want %s prior=%v", out, want, prior)
		}
	}
	assert(SourceCurrent, false)
	repochange.Advance(sess.WorkspacePath)
	assert(SourceCurrent, false)
	write("edited")
	assert(SourceChanged, false)
	write("initial")
	assert(SourceCurrent, false)
	history = append(history, api.Message{Role: api.MessageRoleUser, CreatedAt: when.Add(time.Second)})
	assert(SourceCurrent, true)
	sess.WorkspacePath = t.TempDir()
	path = filepath.Join(sess.WorkspacePath, "source.txt")
	write("initial")
	assert(SourceChanged, true)
	m.SetSourceObservations(nil)
	assert(SourceUnknown, true)
}

func TestSourceObservationsBoundRecordsAndPreserveVerdicts(t *testing.T) {
	when := time.Now().UTC()
	records := []evidence.Record{}
	for i := range 8 {
		records = append(records, checkRecord(fmt.Sprint(i), "content", "roots", when))
	}
	records = append(records, records[7])
	unchecked := checkRecord("not-check", "content", "roots", when)
	unchecked.Artifacts["is_check"] = false
	records = append(records, unchecked)
	out := sourceCheckObservations(records, when.Add(time.Second), "content", "roots")
	if len(out.Checks) != 6 || out.Omitted != 2 {
		t.Fatalf("unbounded or duplicated observations: %+v", out)
	}
	for _, check := range out.Checks {
		if check.Match != SourceCurrent || !check.PriorIntent {
			t.Fatalf("lost independent source/intent facts: %+v", check)
		}
	}
	for _, verdict := range []string{"passed", "failed", "unverifiable"} {
		r := checkRecord(verdict, "", "roots", when)
		r.GateVerdict = verdict
		check := sourceCheckObservations([]evidence.Record{r}, when, "content", "roots").Checks[0]
		if check.Match != SourceUnknown || check.Verdict != verdict {
			t.Fatalf("invented source proof: %+v", check)
		}
	}
}

type unavailableCheckRecords struct{}

func (unavailableCheckRecords) Append(context.Context, string, evidence.Record) error { return nil }
func (unavailableCheckRecords) ReadAll(context.Context, string, string, string, evidence.GateType) ([]evidence.Record, error) {
	return nil, errors.New("unavailable")
}

func TestSourceObservationsReportUnavailableRecords(t *testing.T) {
	sessions := store.NewMemory()
	sess, err := sessions.Create(t.Context(), api.CreateSessionRequest{}, "")
	testutil.FailErr(t, "create session", err)
	m := New(sessions, scope.New(sessions), nil)
	m.SetEvidenceStore(unavailableCheckRecords{})
	m.SetRevisionSource(func(context.Context, string) (string, string) {
		t.Fatal("unread evidence cannot establish current source")
		return "", ""
	})
	out := m.SourceObservations(t.Context(), sess, nil)
	if out == nil || !out.RecordsUnavailable || len(out.Checks) != 0 {
		t.Fatalf("unavailable records became proof: %+v", out)
	}
}
