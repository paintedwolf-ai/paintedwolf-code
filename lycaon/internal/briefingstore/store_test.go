package briefingstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const project = testdbseed.DefaultProjectID

type fixture struct {
	database db.ReadHandle
	store    *Store
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	database := testdbfixture.Open(t, "briefing-store.db")
	testdbseed.InsertProjectRoot(t, database, project, t.TempDir())
	return fixture{database: database, store: NewStore(database, t.TempDir())}
}

func manifest(section, text string) briefing.Manifest {
	return briefing.Manifest{ProfileID: "tool_activity", ProfileVersion: "1", SectionID: section, Template: briefing.Template{ID: section, Version: "1"}, Contract: briefing.OneSentence,
		Items: []briefing.Item{{Key: "evidence", Role: briefing.RoleObservation, SourceKind: "receipt", Inclusion: briefing.IncludedWhole, Text: text}}}
}

func hashOf(t *testing.T, m briefing.Manifest) string {
	t.Helper()
	hash, err := m.Hash()
	testutil.FailErr(t, "hash", err)
	return hash
}

func (f fixture) epoch(t *testing.T) int64 {
	t.Helper()
	epoch, err := f.store.Epoch(t.Context(), project)
	testutil.FailErr(t, "epoch", err)
	return epoch
}

func TestResultsUpgradeOnlyFallbacksAndHonorBounds(t *testing.T) {
	f := newFixture(t)
	ctx, epoch := t.Context(), f.epoch(t)
	m := manifest("purpose", "fixture evidence")
	hash := hashOf(t, m)
	put := func(text string, source briefing.Source) error {
		return f.store.PutResult(ctx, project, epoch, m, hash, briefing.Result{Text: text, Source: source})
	}
	testutil.FailErr(t, "fallback", put("Changed a file.", briefing.SourceFallback))
	testutil.FailErr(t, "upgrade", put("Moved the token check.", briefing.SourceSummarizer))
	testutil.FailErr(t, "late fallback", put("Changed a file.", briefing.SourceFallback))
	testutil.FailErr(t, "second summary", put("Another sentence.", briefing.SourceSummarizer))
	got, err := f.store.GetResults(ctx, project, []string{hash, strings.Repeat("0", 64)})
	testutil.FailErr(t, "get", err)
	if len(got) != 1 || got[hash].Text != "Moved the token check." || got[hash].Source != briefing.SourceSummarizer {
		t.Fatalf("stored %+v", got)
	}
	if err := put(strings.Repeat("a", briefing.OneSentence.MaxRunes+1), briefing.SourceSummarizer); err == nil {
		t.Fatal("over-bound text was stored")
	}
	display, err := f.store.ReadManifest(ctx, project, hash)
	testutil.FailErr(t, "read manifest", err)
	if !strings.Contains(string(display), "fixture evidence") {
		t.Fatalf("manifest blob lost its excerpt: %s", display)
	}
}

func request(target, section, text string) (briefing.Request, briefing.SectionPlan) {
	plan := briefing.SectionPlan{ID: section, Mode: briefing.Generated, Manifest: manifest(section, text), Prompt: briefing.Prompt{System: "s", User: text, MaxTokens: 60}}
	return briefing.Request{ProjectID: project, ProfileID: "tool_activity", Version: "1", TargetKey: target, TargetKind: "activity",
		ScopeFingerprint: "scope-" + target, Policy: briefing.QueuedPolicy, Sections: []briefing.SectionPlan{plan}}, plan
}

type fixedGenerator struct{ text string }

func (g fixedGenerator) Generate(context.Context, briefing.Invocation) (string, string, string, error) {
	return g.text, "provider", "model", nil
}

type publishLog struct {
	seen chan briefing.Snapshot
}

func (p *publishLog) PublishSnapshot(_ context.Context, _ string, snap briefing.Snapshot) {
	p.seen <- snap
}

func runEngine(t *testing.T, f fixture, text string) (*briefing.Engine, *briefing.Scheduler, *publishLog) {
	t.Helper()
	scheduler := briefing.NewScheduler(context.Background())
	t.Cleanup(func() { scheduler.Stop(); scheduler.Wait(context.Background()) })
	log := &publishLog{seen: make(chan briefing.Snapshot, 8)}
	return briefing.NewEngine(scheduler, f.store, fixedGenerator{text: text}, log, nil), scheduler, log
}

func receive(t *testing.T, ch chan briefing.Snapshot) briefing.Snapshot {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		deadline = time.Now().Add(time.Minute)
	}
	select {
	case snap := <-ch:
		return snap
	case <-time.After(time.Until(deadline)):
		t.Fatal("no snapshot published")
		return briefing.Snapshot{}
	}
}

func TestEngineSettlesTargetsThroughTheStore(t *testing.T) {
	f := newFixture(t)
	engine, _, log := runEngine(t, f, "Rebuild from a clean folder.")
	req, _ := request("card", "change", "go build failed")
	pending, err := engine.Request(t.Context(), req)
	testutil.FailErr(t, "request", err)
	done := receive(t, log.seen)
	if done.Status != briefing.StatusComplete || done.RecordID == "" || done.Revision <= pending.Revision {
		t.Fatalf("pending %+v then %+v", pending, done)
	}
	target, err := f.store.GetTarget(t.Context(), project, "tool_activity", "card")
	testutil.FailErr(t, "target", err)
	if target.RecordID != done.RecordID || target.Revision != done.Revision || target.Attempt.Status != "complete" || !target.Sections[0].Resolved || target.TargetKind != "activity" {
		t.Fatalf("target %+v", target)
	}
	record, err := f.store.GetRecord(t.Context(), project, done.RecordID)
	testutil.FailErr(t, "record", err)
	if record.Method != "model" || len(record.Sections) != 1 || record.Origin.AttemptID != done.AttemptID || record.Origin.Trigger != "automatic" {
		t.Fatalf("record %+v", record)
	}
	again, err := engine.Request(t.Context(), req)
	testutil.FailErr(t, "repeat", err)
	if again.Status != briefing.StatusComplete || again.Revision != done.Revision {
		t.Fatalf("an unchanged repeat moved the target: %+v", again)
	}
}

func TestSupersededAttemptDoesNotApply(t *testing.T) {
	f := newFixture(t)
	ctx, epoch := t.Context(), f.epoch(t)
	req, plan := request("t", "change", "evidence")
	hash := briefing.SectionHash{SectionID: "change", ManifestHash: hashOf(t, plan.Manifest)}
	first, err := f.store.StartAttempt(ctx, req, epoch, []briefing.SectionHash{hash})
	testutil.FailErr(t, "start", err)
	joined, err := f.store.StartAttempt(ctx, req, epoch, []briefing.SectionHash{hash})
	testutil.FailErr(t, "join", err)
	if !joined.Joined || joined.ID != first.ID {
		t.Fatalf("pending attempt not joined: %+v %+v", first, joined)
	}
	failed, err := f.store.FailAttempt(ctx, req, first.ID, briefing.Failure{Code: briefing.CodeQueueExpired, Message: "waited"})
	testutil.FailErr(t, "fail", err)
	second, err := f.store.StartAttempt(ctx, req, epoch, []briefing.SectionHash{hash})
	testutil.FailErr(t, "restart", err)
	if second.ID == first.ID || second.Revision <= failed.Revision {
		t.Fatalf("restart reused identity or revision: %+v after %+v", second, failed)
	}
	stale, err := f.store.CompleteAttempt(ctx, req, first.ID, []briefing.SectionHash{hash}, strings.Repeat("a", 64))
	testutil.FailErr(t, "stale complete", err)
	if stale.Applied {
		t.Fatal("a superseded attempt completed the target")
	}
	interrupted, err := f.store.FailInterrupted(ctx)
	testutil.FailErr(t, "interrupt", err)
	target, err := f.store.GetTarget(ctx, project, req.ProfileID, req.TargetKey)
	testutil.FailErr(t, "target", err)
	if interrupted != 1 || target.Attempt.Status != "failed" || target.Attempt.ErrorCode != briefing.CodeInterrupted {
		t.Fatalf("interrupted %d target %+v", interrupted, target.Attempt)
	}
}

func TestGenerationFailureCoolsAutomaticRequests(t *testing.T) {
	f := newFixture(t)
	clock := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)
	f.store.Targets.now = func() time.Time { return clock }
	ctx, epoch := t.Context(), f.epoch(t)
	req, plan := request("t", "change", "evidence")
	hashes := []briefing.SectionHash{{SectionID: "change", ManifestHash: hashOf(t, plan.Manifest)}}
	failWith := func(code string) briefing.Attempt {
		t.Helper()
		started, err := f.store.StartAttempt(ctx, req, epoch, hashes)
		testutil.FailErr(t, "start", err)
		_, err = f.store.FailAttempt(ctx, req, started.ID, briefing.Failure{Code: code, Message: "down"})
		testutil.FailErr(t, "fail", err)
		return started
	}
	first := failWith(briefing.CodeGenerationFailed)
	clock = clock.Add(briefing.FailureCooldown - time.Second)
	cooled, err := f.store.StartAttempt(ctx, req, epoch, hashes)
	testutil.FailErr(t, "cooled", err)
	if cooled.Cooling == nil || cooled.Cooling.Code != briefing.CodeGenerationFailed || cooled.ID != first.ID {
		t.Fatalf("automatic request inside the cooldown started work: %+v", cooled)
	}
	changed, _ := request("t", "change", "new evidence")
	fresh, err := f.store.StartAttempt(ctx, changed, epoch, []briefing.SectionHash{{SectionID: "change", ManifestHash: hashOf(t, changed.Sections[0].Manifest)}})
	testutil.FailErr(t, "changed", err)
	if fresh.Cooling != nil || fresh.ID == first.ID {
		t.Fatalf("changed material waited out the cooldown: %+v", fresh)
	}
	_, err = f.store.FailAttempt(ctx, req, fresh.ID, briefing.Failure{Code: briefing.CodeGenerationFailed, Message: "down"})
	testutil.FailErr(t, "fail changed", err)
	manual := req
	manual.Manual = true
	retried, err := f.store.StartAttempt(ctx, manual, epoch, hashes)
	testutil.FailErr(t, "manual", err)
	if retried.Cooling != nil {
		t.Fatalf("manual retry waited out the cooldown: %+v", retried)
	}
	_, err = f.store.FailAttempt(ctx, req, retried.ID, briefing.Failure{Code: briefing.CodeGenerationFailed, Message: "down"})
	testutil.FailErr(t, "fail manual", err)
	clock = clock.Add(briefing.FailureCooldown)
	later, err := f.store.StartAttempt(ctx, req, epoch, hashes)
	testutil.FailErr(t, "after cooldown", err)
	if later.Cooling != nil || later.Joined {
		t.Fatalf("cooldown never ended: %+v", later)
	}
}

func seedCheckpoint(t *testing.T, f fixture) string {
	t.Helper()
	session, err := store.NewSQL(f.database).Create(t.Context(), api.CreateSessionRequest{}, project)
	testutil.FailErr(t, "session", err)
	_, err = f.database.ExecContext(t.Context(), `INSERT INTO checkpoints(id, session_id, project_id, kind, status, created_at) VALUES ('cp1', ?, ?, 'tool_approval', 'approved', ?)`,
		session.ID, project, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "checkpoint", err)
	return "cp1"
}

func settle(t *testing.T, f fixture, epoch int64, target, text string) string {
	t.Helper()
	req, plan := request(target, "change", text)
	hash := briefing.SectionHash{SectionID: "change", ManifestHash: hashOf(t, plan.Manifest)}
	testutil.FailErr(t, "put result", f.store.PutResult(t.Context(), project, epoch, plan.Manifest, hash.ManifestHash, briefing.Result{Text: "Ran it.", Source: briefing.SourceSummarizer}))
	record := briefing.Record{ID: briefing.RecordID(req.ProfileID, req.Version, []briefing.SectionHash{hash}), ProfileID: req.ProfileID, Version: req.Version, Sections: []briefing.SectionHash{hash}}
	testutil.FailErr(t, "put record", f.store.PutRecord(t.Context(), project, epoch, record))
	settled, err := f.store.Resolve(t.Context(), req, epoch, []briefing.SectionHash{hash}, record.ID)
	testutil.FailErr(t, "resolve", err)
	if !settled.Applied {
		t.Fatal("resolve did not apply")
	}
	return record.ID
}

func count(t *testing.T, f fixture, table string) int {
	t.Helper()
	var n int
	testutil.FailErr(t, "count "+table, f.database.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func TestReplacedRecordsAreReleasedUnlessPinned(t *testing.T) {
	f := newFixture(t)
	ctx, epoch := t.Context(), f.epoch(t)
	checkpoint := seedCheckpoint(t, f)
	pinned := settle(t, f, epoch, "a", "first evidence")
	testutil.FailErr(t, "pin", f.store.PinCheckpoint(ctx, project, checkpoint, pinned))
	settle(t, f, epoch, "a", "second evidence")
	settle(t, f, epoch, "a", "third evidence")
	if records, manifests := count(t, f, "briefing_records"), count(t, f, "briefing_manifests"); records != 2 || manifests != 2 {
		t.Fatalf("expected the pinned and current records only: %d records, %d manifests", records, manifests)
	}
	if _, err := f.store.GetRecord(ctx, project, pinned); err != nil {
		t.Fatalf("pinned record released: %v", err)
	}
	_, err := f.database.ExecContext(ctx, `DELETE FROM checkpoints WHERE id = ?`, checkpoint)
	testutil.FailErr(t, "delete checkpoint", err)
	testutil.FailErr(t, "sweep", f.store.Sweep(ctx))
	if records := count(t, f, "briefing_records"); records != 1 {
		t.Fatalf("unpinned record survived the sweep: %d", records)
	}
}

func TestPurgeAdvancesTheEpochAndRefusesStaleWrites(t *testing.T) {
	f := newFixture(t)
	ctx, epoch := t.Context(), f.epoch(t)
	settle(t, f, epoch, "a", "evidence")
	next, err := f.store.Purge(ctx, project, []string{"tool_activity"})
	testutil.FailErr(t, "purge", err)
	if next != epoch+1 || count(t, f, "briefing_targets") != 0 || count(t, f, "briefing_records") != 0 || count(t, f, "briefing_results") != 0 {
		t.Fatalf("purge left state at epoch %d", next)
	}
	var queued int
	testutil.FailErr(t, "reclaim queue", f.database.QueryRowContext(ctx, `SELECT count(*) FROM content_blob_reclaim_queue WHERE project_id = ?`, project).Scan(&queued))
	if queued != 1 {
		t.Fatalf("deleted manifest blob not queued for reclaim: %d", queued)
	}
	m := manifest("change", "late")
	if err := f.store.PutResult(ctx, project, epoch, m, hashOf(t, m), briefing.Result{Text: "Late.", Source: briefing.SourceSummarizer}); !errors.Is(err, briefing.ErrStaleEpoch) {
		t.Fatalf("stale write: %v", err)
	}
	req, _ := request("a", "change", "late")
	if _, err := f.store.StartAttempt(ctx, req, epoch, nil); !errors.Is(err, briefing.ErrStaleEpoch) {
		t.Fatalf("stale attempt: %v", err)
	}
}

func TestApprovalIntentLinksWithoutMovingTheTarget(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	settle(t, f, f.epoch(t), "a", "evidence")
	intent := Intent{Text: "git push", CheckpointID: "cp", ActionID: "call", Scope: "command"}
	testutil.FailErr(t, "link", f.store.LinkApprovalIntent(ctx, project, "tool_activity", "a", intent))
	linked, err := f.store.GetTarget(ctx, project, "tool_activity", "a")
	testutil.FailErr(t, "target", err)
	testutil.FailErr(t, "relink", f.store.LinkApprovalIntent(ctx, project, "tool_activity", "a", intent))
	again, err := f.store.GetTarget(ctx, project, "tool_activity", "a")
	testutil.FailErr(t, "target again", err)
	if linked.Facts.Intent == nil || *linked.Facts.Intent != intent || again.Revision != linked.Revision || again.RecordID != linked.RecordID {
		t.Fatalf("link %+v then %+v", linked, again)
	}
}

func TestApprovedRationaleLinksOnlyFromApprovedCards(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	session, err := store.NewSQL(f.database).Create(ctx, api.CreateSessionRequest{}, project)
	testutil.FailErr(t, "session", err)
	insert := func(id, status string) {
		_, err := f.database.ExecContext(ctx, `INSERT INTO checkpoints(id, session_id, project_id, kind, status, payload_json, created_at) VALUES (?, ?, ?, 'tool_approval', ?, ?, ?)`,
			id, session.ID, project, status, `{"tool_call_id":"call","ai_rationale":"Pushes the fix."}`, db.FormatTime(time.Now().UTC()))
		testutil.FailErr(t, "checkpoint "+id, err)
	}
	settle(t, f, f.epoch(t), "a", "evidence")
	req, _ := request("a", "change", "evidence")
	req.Action = &briefing.ActionRef{SessionID: session.ID, ToolCallID: "call"}
	insert("pending-card", "pending")
	testutil.FailErr(t, "link pending", f.store.LinkApprovedRationale(ctx, req))
	if target, _ := f.store.GetTarget(ctx, project, "tool_activity", "a"); target.Facts.Intent != nil {
		t.Fatalf("a pending card linked: %+v", target.Facts.Intent)
	}
	insert("approved-card", "approved")
	testutil.FailErr(t, "link", f.store.LinkApprovedRationale(ctx, req))
	linked, err := f.store.GetTarget(ctx, project, "tool_activity", "a")
	testutil.FailErr(t, "target", err)
	testutil.FailErr(t, "relink", f.store.LinkApprovedRationale(ctx, req))
	again, err := f.store.GetTarget(ctx, project, "tool_activity", "a")
	testutil.FailErr(t, "target again", err)
	if linked.Facts.Intent == nil || linked.Facts.Intent.CheckpointID != "approved-card" || linked.Facts.Intent.Text != "Pushes the fix." || again.Revision != linked.Revision {
		t.Fatalf("link %+v then revision %d -> %d", linked.Facts.Intent, linked.Revision, again.Revision)
	}
}
