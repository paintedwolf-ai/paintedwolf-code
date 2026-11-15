package workermessaging_test

import (
	"context"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workermessaging"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type multiWorkerMessagingFixture struct {
	sqlDB   *db.Store
	records *workerrecords.Store
	coord   workerrecords.Actor
	alpha   workerrecords.Actor
	beta    workerrecords.Actor
	alphaID string
	betaID  string
}

func newMultiWorkerMessagingFixture(t *testing.T) *multiWorkerMessagingFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "messaging-qual.db")
	testdbseed.InsertSession(t, sqlDB, "coord", testdbseed.DefaultProjectID)
	now := db.FormatTime(time.Now().UTC())
	for _, id := range []string{"child-alpha", "child-beta"} {
		_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
			VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', 'implementer', ?, ?, ?)`, id, testdbseed.DefaultProjectID, now, now, now)
		testutil.FailErr(t, "insert child "+id, err)
	}
	records := workerrecords.New(sqlDB)
	f := &multiWorkerMessagingFixture{
		sqlDB:   sqlDB,
		records: records,
		coord:   workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"},
		alpha:   workerrecords.Actor{SessionID: "child-alpha", Role: workerrecords.RoleWorker, PrincipalID: "child-alpha"},
		beta:    workerrecords.Actor{SessionID: "child-beta", Role: workerrecords.RoleWorker, PrincipalID: "child-beta"},
	}
	wa, err := records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-alpha"}, workerrecords.WorkerSpec{
		Name: "alpha", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "child-alpha", Brief: "Fix parser alpha",
	})
	testutil.FailErr(t, "create alpha", err)
	wb, err := records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-beta"}, workerrecords.WorkerSpec{
		Name: "beta", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "child-beta", Brief: "Fix parser beta",
	})
	testutil.FailErr(t, "create beta", err)
	f.alphaID, f.betaID = wa.WorkerID, wb.WorkerID

	for _, pair := range []struct{ job, child, wid string }{
		{"job-alpha-1", "child-alpha", f.alphaID},
		{"job-beta-1", "child-beta", f.betaID},
	} {
		_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
			VALUES (?, ?, 'coord', ?, 'implementer', 'running', 'work', 'work', ?)`, pair.job, testdbseed.DefaultProjectID, pair.child, now)
		testutil.FailErr(t, "insert "+pair.job, err)
		_, err = records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-" + pair.job}, pair.wid, workerrecords.RoundSpec{
			JobID: pair.job, ExecutionTarget: "local",
		})
		testutil.FailErr(t, "admit "+pair.job, err)
	}
	return f
}

func TestMultiWorkerOverflowProviderFailureRestartAndHistoryIsolation(t *testing.T) {
	f := newMultiWorkerMessagingFixture(t)
	delivery := &workermessaging.Delivery{Records: f.records}

	for i := 1; i <= 40; i++ {
		_, err := f.records.AppendMessage(t.Context(), f.alpha, workerrecords.Op{ID: "alpha-out-" + strconv.Itoa(i)}, f.alphaID, workerrecords.MessageSpec{
			Body:       "alpha-obs-" + strconv.Itoa(i),
			NeedsReply: i == 40,
		})
		testutil.FailErr(t, "append alpha", err)
	}
	for i := 1; i <= 15; i++ {
		_, err := f.records.AppendMessage(t.Context(), f.beta, workerrecords.Op{ID: "beta-out-" + strconv.Itoa(i)}, f.betaID, workerrecords.MessageSpec{
			Body: "beta-obs-" + strconv.Itoa(i),
		})
		testutil.FailErr(t, "append beta", err)
	}

	// Reading history before or during uncommitted delivery never advances unread cursors.
	hist, err := f.records.History(t.Context(), f.coord, f.alphaID, 20, 10)
	testutil.FailErr(t, "history alpha", err)
	if len(hist) != 10 || hist[0].Seq != 19 || hist[9].Seq != 10 {
		t.Fatalf("history slice = len %d [%d..%d], want 10 [19..10]", len(hist), hist[0].Seq, hist[len(hist)-1].Seq)
	}

	first, err := delivery.PrepareMessages(t.Context(), &api.Session{ID: "coord"})
	testutil.FailErr(t, "prepare first", err)
	if len(first.Lines) != 32 || !first.More || first.Lines[0].Worker != "alpha" || first.Lines[0].Seq != 1 || first.Lines[31].Seq != 32 {
		t.Fatalf("first page = %+v", first)
	}

	// Simulate provider failure (no CommitMessages) + history inspection + coordinator restart.
	_, err = f.records.History(t.Context(), f.coord, f.betaID, 0, 5)
	testutil.FailErr(t, "history beta", err)
	restartedRecords := workerrecords.New(f.sqlDB)
	restartedDelivery := &workermessaging.Delivery{Records: restartedRecords}

	replayed, err := restartedDelivery.PrepareMessages(t.Context(), &api.Session{ID: "coord"})
	testutil.FailErr(t, "replay after restart", err)
	if len(replayed.Pages) != 1 || replayed.Pages[0].PageID != first.Pages[0].PageID || len(replayed.Lines) != 32 || replayed.Lines[0].Seq != 1 {
		t.Fatalf("replay = %+v, want page %s", replayed, first.Pages[0].PageID)
	}

	// Commit page 1; page 2 delivers the remaining 8 alpha messages and all 15 beta messages.
	testutil.FailErr(t, "commit page 1", restartedDelivery.CommitMessages(t.Context(), "coord", replayed))
	second, err := restartedDelivery.PrepareMessages(t.Context(), &api.Session{ID: "coord"})
	testutil.FailErr(t, "prepare second", err)
	if len(second.Lines) != 23 || second.More {
		t.Fatalf("second page = %d lines (more=%v), want 23 (more=false)", len(second.Lines), second.More)
	}
	if second.Lines[0].Worker != "alpha" || second.Lines[0].Seq != 33 || !second.Lines[7].NeedsReply || second.Lines[8].Worker != "beta" || second.Lines[22].Seq != 15 {
		t.Fatalf("second page ordering = first:%+v eighth:%+v ninth:%+v last:%+v", second.Lines[0], second.Lines[7], second.Lines[8], second.Lines[22])
	}
	testutil.FailErr(t, "commit page 2", restartedDelivery.CommitMessages(t.Context(), "coord", second))

	// Compaction generations do not replay committed coordinator pages.
	for _, gen := range []int{1, 2} {
		after, err := restartedDelivery.PrepareMessages(t.Context(), &api.Session{ID: "coord", CompactionGeneration: gen})
		testutil.FailErr(t, "after compaction", err)
		if len(after.Lines) != 0 || len(after.Pages) != 0 {
			t.Fatalf("gen %d replayed committed messages: %+v", gen, after)
		}
	}
}

func TestWorkerInstructionOverflowUnappliedSettlementRaceAndRestart(t *testing.T) {
	f := newMultiWorkerMessagingFixture(t)
	delivery := &workermessaging.Delivery{Records: f.records}
	alphaSess := &api.Session{ID: "child-alpha", ParentSessionID: "coord"}

	for i := 1; i <= 70; i++ {
		_, err := f.records.AppendMessage(t.Context(), f.coord, workerrecords.Op{ID: "in-" + strconv.Itoa(i)}, f.alphaID, workerrecords.MessageSpec{
			Body: "step-" + strconv.Itoa(i),
		})
		testutil.FailErr(t, "append instruction", err)
	}

	page1, err := delivery.PrepareMessages(t.Context(), alphaSess)
	testutil.FailErr(t, "prepare worker page 1", err)
	if len(page1.Lines) != workerrecords.MaxPageMessages || !page1.More || page1.Lines[0].Seq != 1 || page1.Lines[63].Seq != 64 {
		t.Fatalf("worker page 1 = len %d more=%v", len(page1.Lines), page1.More)
	}

	// Provider failure + restart replays page 1 identically; CommitMessages advances to seq 64.
	restarted := &workermessaging.Delivery{Records: workerrecords.New(f.sqlDB)}
	replay1, err := restarted.PrepareMessages(t.Context(), alphaSess)
	testutil.FailErr(t, "replay worker page 1", err)
	if len(replay1.Pages) != 1 || replay1.Pages[0].PageID != page1.Pages[0].PageID {
		t.Fatalf("worker replay mismatch: %+v vs %+v", replay1.Pages, page1.Pages)
	}
	testutil.FailErr(t, "commit worker page 1", restarted.CommitMessages(t.Context(), alphaSess.ID, replay1))

	// Open page 2 (seq 65..70) without committing, then race round delivery settlement.
	page2, err := restarted.PrepareMessages(t.Context(), alphaSess)
	testutil.FailErr(t, "prepare worker page 2", err)
	if len(page2.Lines) != 6 || page2.Lines[0].Seq != 65 || page2.Lines[5].Seq != 70 {
		t.Fatalf("worker page 2 = %+v", page2)
	}

	now := db.FormatTime(time.Now().UTC())
	_, err = f.sqlDB.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES ('result-alpha-1', 'job-alpha-1', 'complete', ?)`, now)
	testutil.FailErr(t, "insert result", err)
	n, err := db.New(f.sqlDB).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{
		SettledAt: db.NullString(now), JobID: "job-alpha-1",
	})
	testutil.FailErr(t, "settle round 1", err)
	if n != 1 {
		t.Fatalf("settle round 1 touched %d rows", n)
	}

	unapplied, err := f.records.NotApplied(t.Context(), f.coord, f.alphaID, 1)
	testutil.FailErr(t, "not applied", err)
	if len(unapplied) != 6 || unapplied[0].Seq != 65 || unapplied[5].Seq != 70 {
		t.Fatalf("unapplied = %+v, want seq 65..70", unapplied)
	}

	// Settled worker receives neither round frame nor undelivered round-1 pages.
	settledPage, err := restarted.PrepareMessages(t.Context(), alphaSess)
	testutil.FailErr(t, "settled prepare", err)
	if len(settledPage.Lines) != 0 {
		t.Fatalf("settled worker received messages: %+v", settledPage)
	}
	if _, ok, err := restarted.RoundFrame(t.Context(), alphaSess); err != nil || ok {
		t.Fatalf("settled worker kept active round frame: ok=%v err=%v", ok, err)
	}
}

type saturatedFindingsSpy struct {
	pages     [][]inject.SiblingNote
	cursor    int
	committed int
}

func (s *saturatedFindingsSpy) PrepareSiblingNotes(context.Context, string, int) (inject.SiblingNotePage, error) {
	if s.cursor >= len(s.pages) {
		return inject.SiblingNotePage{Cursor: int64(s.cursor)}, nil
	}
	notes := s.pages[s.cursor]
	return inject.SiblingNotePage{Notes: notes, Cursor: int64(s.cursor + 1)}, nil
}

func (s *saturatedFindingsSpy) CommitSiblingNotes(_ context.Context, _, _ string, cursor int64, notes []inject.SiblingNote) error {
	s.cursor = int(cursor)
	s.committed += len(notes)
	return nil
}

type staticWorkerLegCtx struct {
	leg inject.WorkerLegContext
}

func (c staticWorkerLegCtx) BuildWorkerLegContext(context.Context, *api.Session, string) (inject.WorkerLegContext, bool) {
	return c.leg, true
}

func TestFindingsAndMessagesStreamsNeverStarveOrCrossAcknowledge(t *testing.T) {
	f := newMultiWorkerMessagingFixture(t)
	delivery := &workermessaging.Delivery{Records: f.records}

	for i := 1; i <= 70; i++ {
		_, err := f.records.AppendMessage(t.Context(), f.coord, workerrecords.Op{ID: "sat-in-" + strconv.Itoa(i)}, f.alphaID, workerrecords.MessageSpec{
			Body: "instruction-token-" + strconv.Itoa(i),
		})
		testutil.FailErr(t, "append instruction", err)
	}

	findings := &saturatedFindingsSpy{
		pages: [][]inject.SiblingNote{
			{{ID: 101, HasBody: true, Agent: "beta", Summary: "finding-page-one", Ref: "a.go:1"}},
			{{ID: 102, HasBody: true, Agent: "beta", Summary: "finding-page-two", Ref: "b.go:2"}},
		},
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	moduleRoot := filepath.Join(filepath.Dir(file), "..", "..")
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: moduleRoot})
	inj := prompts.NewInjectRenderer(pe)

	eng := &assembly.AssemblyEngine{}
	eng.SetDeps(assembly.AssemblyDeps{
		Prompts: pe,
		Injects: inj,
		Limits:  func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		Workers: assembly.Workers{Leg: staticWorkerLegCtx{leg: inject.WorkerLegContext{LegID: "job-alpha-1", Checklist: []string{"step"}}}},
		Deliveries: assembly.Deliveries{
			SiblingNotes: findings,
			Messages:     delivery,
		},
	})
	sess := &api.Session{
		ID: "child-alpha", ParentSessionID: "coord", ProjectID: testdbseed.DefaultProjectID,
		Posture: api.SessionPostureBuild, AgentType: "implementer", WorkspacePath: t.TempDir(),
	}

	// Failed turn (no CommitTurnContext): both findings page 1 and message page 1 appear and replay on retry.
	for attempt := range 2 {
		eng.BeginPromptTurn(sess.ID)
		msgs, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{{Role: api.MessageRoleUser, Content: "work"}}, nil)
		testutil.FailErr(t, "assemble turn 1", err)
		assertWorkerStreamParts(t, msgs, "finding-page-one", "instruction-token-1", "instruction-token-70")
		if findings.committed != 0 {
			t.Fatalf("attempt %d committed findings before response", attempt)
		}
	}

	// Commit turn 1: findings advances to page 2 and messages advances to page 2 (65..70), while RoundFrame still retains 1..70!
	testutil.FailErr(t, "commit turn 1", eng.CommitTurnContext(t.Context(), sess.ID, "resp-1"))
	if findings.committed != 1 {
		t.Fatalf("findings committed = %d, want 1", findings.committed)
	}

	eng.BeginPromptTurn(sess.ID)
	msgs2, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{{Role: api.MessageRoleUser, Content: "continue"}}, nil)
	testutil.FailErr(t, "assemble turn 2", err)
	assertWorkerStreamParts(t, msgs2, "finding-page-two", "instruction-token-65", "instruction-token-1")
	testutil.FailErr(t, "commit turn 2", eng.CommitTurnContext(t.Context(), sess.ID, "resp-2"))
	if findings.committed != 2 {
		t.Fatalf("findings committed = %d, want 2", findings.committed)
	}
}

func assertWorkerStreamParts(t *testing.T, msgs []api.Message, wantFinding, wantPageToken, wantRoundToken string) {
	t.Helper()
	var sawFinding, sawMessagePage, sawRoundFrame bool
	for _, msg := range msgs {
		for _, part := range msg.ContentParts {
			switch part.Source {
			case "sibling_notes":
				if part.Authority != api.ContentAuthorityNone || part.TrustTier != api.ContentTrustTierUntrusted {
					t.Fatalf("sibling_notes authority = %+v", part)
				}
				sawFinding = sawFinding || strings.Contains(part.Content, wantFinding)
			case "worker_messages":
				if part.Authority != api.ContentAuthorityUser || part.TrustTier != api.ContentTrustTierTrusted {
					t.Fatalf("worker_messages authority = %+v", part)
				}
				if strings.Contains(part.Content, "Instructions this round:") {
					sawRoundFrame = sawRoundFrame || strings.Contains(part.Content, wantRoundToken)
				} else {
					sawMessagePage = sawMessagePage || strings.Contains(part.Content, wantPageToken)
				}
			}
		}
	}
	if !sawFinding || !sawMessagePage || !sawRoundFrame {
		t.Fatalf("missing stream in assembled request: finding=%v messagePage=%v roundFrame=%v", sawFinding, sawMessagePage, sawRoundFrame)
	}
}
