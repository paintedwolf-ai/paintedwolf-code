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
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
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

func TestSourceObservationsIntentBoundaryUsesUserTimestamp(t *testing.T) {
	when := time.Now().UTC()
	history := []api.Message{{Role: api.MessageRoleUser, CreatedAt: when}}
	for _, next := range []api.Message{
		{Role: api.MessageRoleTool, CreatedAt: when.Add(time.Second)},
		{Role: api.MessageRoleUser, Visibility: api.MessageVisibilityInternal, CreatedAt: when.Add(2 * time.Second)},
		{Role: api.MessageRoleUser, Kind: api.MessageKindUserContinuation, CreatedAt: when.Add(3 * time.Second)},
	} {
		if got := boundaryTime(history); !got.Equal(when) {
			t.Fatalf("intent boundary=%v, want %v", got, when)
		}
		history = append(history, next)
	}
	if got := boundaryTime(history); !got.Equal(when) {
		t.Fatalf("host continuation moved intent boundary: %v", got)
	}
	if got := boundaryTime(nil); !got.IsZero() {
		t.Fatalf("absent intent acquired timestamp: %v", got)
	}
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
	history := []api.Message{{Role: api.MessageRoleUser, Content: "Check the source", CreatedAt: when.Add(-time.Second)}}
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
	history = append(history, api.Message{Role: api.MessageRoleUser, Content: "Change the source", CreatedAt: when.Add(time.Second)})
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

func TestObservedDependencyAndIncompleteAttributionInvalidateVerificationChecks(t *testing.T) {
	ctx := t.Context()
	sessions := store.NewMemory()
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "")
	testutil.FailErr(t, "create session", err)
	sess.ProjectID, sess.WorkspacePath = "project", t.TempDir()

	m := New(sessions, scope.New(sessions), nil)
	m.SetDataDir(t.TempDir())
	m.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	m.SetRevisionSource(func(context.Context, string) (string, string) {
		return "rev-1", "root-1"
	})

	facts := observation.LiveMountVisibilityFacts("fuse-linux", true, true, true, observation.CompletenessComplete)
	tracker, err := observation.NewTracker(observation.TrackerConfig{ViewID: "view-verify", Facts: facts})
	testutil.FailErr(t, "NewTracker", err)
	m.SetObservationTracker(sess.ID, tracker)

	depPath := layeredfs.QualifiedPath{RootID: "other-root", Rel: "shared/schema.json"}
	tracker.RecordServerFSObservation(layeredfs.ObservationEvent{
		Path:   depPath,
		Layer:  layeredfs.LayerTrunk,
		Kind:   layeredfs.ObserveRead,
		Size:   16,
		SHA256: "sha-schema-v1",
	})

	when := time.Now().UTC()
	rec := checkRecord("chk-cross-root", "rev-1", "root-1", when)
	rec.RunID = sess.ID
	testutil.FailErr(t, "append check", m.evidenceStore.Append(ctx, m.EvidenceRootFor(sess), rec))
	_, boundOK := m.BindCheckObservation(sess.ID, "chk-cross-root", "ckpt-1", []string{"primary", "other-root"})
	if !boundOK {
		t.Fatal("expected BindCheckObservation to succeed")
	}

	history := []api.Message{{Role: api.MessageRoleUser, Content: "Verify", CreatedAt: when.Add(-time.Second)}}

	// Unrelated trunk change outside observed set with complete attribution keeps check current.
	tracker.RecordTrunkChange(observation.TrunkChangeEvent{
		Path:      layeredfs.QualifiedPath{RootID: "primary", Rel: "docs/readme.md"},
		Kind:      observation.TrunkWriteInPlace,
		NewSHA256: "sha-readme-v2",
		NewSize:   20,
	})
	obs1 := m.SourceObservations(ctx, sess, history)
	if obs1 == nil || len(obs1.Checks) != 1 || obs1.Checks[0].Match != SourceCurrent {
		t.Fatalf("expected unrelated change outside complete observed set to keep SourceCurrent, got %+v", obs1)
	}
	passed, _, _ := m.GateState(ctx, sess, history)
	if !passed {
		t.Fatal("expected GateState to pass before observed dependency change")
	}

	// Cross-root observed dependency change marks check stale and fails GateState.
	tracker.RecordTrunkChange(observation.TrunkChangeEvent{
		Path:      depPath,
		Kind:      observation.TrunkAtomicReplace,
		NewSHA256: "sha-schema-v2",
		NewSize:   18,
	})
	obs2 := m.SourceObservations(ctx, sess, history)
	if obs2 == nil || len(obs2.Checks) != 1 || obs2.Checks[0].Match != SourceChanged {
		t.Fatalf("expected cross-root dependency change to mark SourceChanged, got %+v", obs2)
	}
	if obs2.Checks[0].StaleReason != observation.StaleReasonObservedDependencyChanged {
		t.Fatalf("expected StaleReasonObservedDependencyChanged, got %q", obs2.Checks[0].StaleReason)
	}
	if obs2.TrunkChanged == nil || obs2.TrunkChanged.GrantsAuthority || obs2.TrunkChanged.EstablishesMergeBase {
		t.Fatalf("unexpected trunk.changed notice: %+v", obs2.TrunkChanged)
	}
	if len(obs2.PromotionPreviewHints) == 0 || obs2.PromotionPreviewHints[0].SubstitutesExactBaseCheck {
		t.Fatalf("unexpected promotion preview hints: %+v", obs2.PromotionPreviewHints)
	}
	passedAfter, repairAfter, _ := m.GateState(ctx, sess, history)
	if passedAfter || !repairAfter {
		t.Fatalf("expected stale observed check to fail GateState (passed=%v repair=%v)", passedAfter, repairAfter)
	}
}
