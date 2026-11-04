package sourcebrief

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// briefLedger is a real ledger with windows w1 (session s1) and w2 (session s2).
type briefLedger struct {
	t      *testing.T
	ledger *sourceledger.Store
	next   int
}

func newBriefLedger(t *testing.T) *briefLedger {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, "p1")
	now := db.FormatTime(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO project_roots
 (id, project_id, path, label, is_primary, added_at, kind)
 VALUES ('r1','p1','/tmp/brief-root','root',1,?,'attached')`, now)
	testutil.FailErr(t, "seed root", err)
	for i, session := range []string{"s1", "s2"} {
		_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO source_command_windows
 (id, project_id, session_id, tool_name, command_line, state, ordinal, started_ts)
 VALUES (?,'p1',?,'command','npm run build','ended',?,?)`, fmt.Sprintf("w%d", i+1), session, i+1, now)
		testutil.FailErr(t, "seed window", err)
	}
	return &briefLedger{t: t, ledger: sourceledger.New(sqlDB, t.TempDir())}
}

func (l *briefLedger) checkpoint(turn int) {
	l.t.Helper()
	_, err := l.ledger.Checkpoints.CreateStructuralCheckpoint(l.t.Context(), sourceledger.StructuralCheckpointInput{
		ProjectID: "p1", Kind: sourceledger.CheckpointTurn, Label: "Turn start", SessionID: "s1", Turn: turn,
	})
	testutil.FailErr(l.t, "create checkpoint", err)
}

// record lands one effect shaped by its structured columns.
func (l *briefLedger) record(path string, in sourceledger.RecordInput) {
	l.t.Helper()
	l.next++
	in.RecordLocation = sourceledger.RecordLocation{RootID: "r1", Path: path}
	in.ProjectID, in.Op = "p1", api.SourceChangeOpWrite
	in.Before, in.After = []byte(fmt.Sprint(l.next)), []byte(fmt.Sprint(l.next+1))
	testutil.FailErr(l.t, "record effect", l.ledger.Record(l.t.Context(), in))
}

func (l *briefLedger) ownCommand(path string) {
	l.t.Helper()
	l.record(path, sourceledger.RecordInput{Origin: api.SourceChangeOriginExternal, SessionID: "s1", CommandWindowID: "w1"})
}

func (l *briefLedger) person(path string) {
	l.t.Helper()
	l.record(path, sourceledger.RecordInput{Origin: api.SourceChangeOriginUser})
}

func (l *briefLedger) build() inject.SourceChangeBrief {
	l.t.Helper()
	service := New(briefWindowStore{turn: 2}, briefRootList(briefRoots()))
	service.SetSources(tools.SourceHistory{Files: l.ledger.History, Git: l.ledger.Git, Authorship: l.ledger.Walk}, l.ledger.Checkpoints)
	return service.Build(l.t.Context(), &api.Session{ID: "s1", ProjectID: "p1"})
}

// A build past the effect cap, newer than a person's edit, must not hide it.
func TestACOC01OwnEffectsPastTheCapCannotDisplaceAPersonsEdit(t *testing.T) {
	l := newBriefLedger(t)
	l.checkpoint(1)
	l.person("a.go")
	for i := range sourceChangeBriefEffectCap + 5 {
		l.ownCommand(fmt.Sprintf("gen/%03d.go", i))
	}
	l.checkpoint(2)
	brief := l.build()
	if len(brief.Files) != sourceChangeBriefFileCap || brief.Files[0].Path != "a.go" || brief.Files[0].DuringYourCommand {
		t.Fatalf("rows = %+v, want the person's edit first", brief.Files)
	}
	if brief.Truncated || !brief.OwnTruncated {
		t.Fatalf("brief = %+v, want only the own class reported as overflowing", brief)
	}
}

func TestBriefOverflowIsReportedPerClass(t *testing.T) {
	limit := sourceChangeBriefEffectCap
	for _, tc := range []struct {
		name                 string
		person, own          int
		wantForeign, wantOwn bool
	}{
		{"person past the cap", limit + 1, 1, true, false},
		{"own at the cap", 1, limit, false, false},
		{"both past the cap", limit + 1, limit + 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newBriefLedger(t)
			l.checkpoint(1)
			for i := range tc.person {
				l.person(fmt.Sprintf("edit/%03d.go", i))
			}
			for i := range tc.own {
				l.ownCommand(fmt.Sprintf("gen/%03d.go", i))
			}
			l.checkpoint(2)
			brief := l.build()
			if brief.Truncated != tc.wantForeign || brief.OwnTruncated != tc.wantOwn {
				t.Fatalf("truncated = %v, own = %v, want %v, %v", brief.Truncated, brief.OwnTruncated, tc.wantForeign, tc.wantOwn)
			}
		})
	}
}

// The SQL classes and Effect's own predicates must place every shape alike.
func TestBriefClassQueryAgreesWithTheEffectPredicates(t *testing.T) {
	l := newBriefLedger(t)
	l.checkpoint(1)
	type class int
	const (
		neither class = iota
		foreign
		own
	)
	shapes := []struct {
		path string
		in   sourceledger.RecordInput
		want class
	}{
		{"person.go", sourceledger.RecordInput{Origin: api.SourceChangeOriginUser}, foreign},
		{"own-write.go", sourceledger.RecordInput{Origin: api.SourceChangeOriginAgent, SessionID: "s1"}, neither},
		{"own-write-in-window.go", sourceledger.RecordInput{Origin: api.SourceChangeOriginAgent, SessionID: "s1", CommandWindowID: "w1"}, neither},
		{"own-window.go", sourceledger.RecordInput{Origin: api.SourceChangeOriginExternal, SessionID: "s1", CommandWindowID: "w1"}, own},
		{"other-window.go", sourceledger.RecordInput{Origin: api.SourceChangeOriginExternal, SessionID: "s2", CommandWindowID: "w2"}, foreign},
		{"other-agent.go", sourceledger.RecordInput{Origin: api.SourceChangeOriginAgent, SessionID: "s2"}, foreign},
		{"drift.go", sourceledger.RecordInput{Origin: api.SourceChangeOriginExternal}, foreign},
		{"own-session-no-window.go", sourceledger.RecordInput{Origin: api.SourceChangeOriginExternal, SessionID: "s1"}, foreign},
		{"worker.go", sourceledger.RecordInput{Origin: api.SourceChangeOriginAgent, BranchID: sourcebranch.ID("job-1"), JobID: "job-1"}, neither},
		{"worktree.go", sourceledger.RecordInput{Origin: api.SourceChangeOriginUser, BranchID: sourcebranch.ForWorktree("w1")}, foreign},
	}
	for _, shape := range shapes {
		l.record(shape.path, shape.in)
	}
	read := func(duringCommand bool) map[string]sourceledger.Effect {
		effects, err := l.ledger.History.EffectsForBriefClass(t.Context(), "p1", "s1", duringCommand, 0, math.MaxInt64, 100)
		testutil.FailErr(t, "read class", err)
		byPath := make(map[string]sourceledger.Effect, len(effects))
		for _, effect := range effects {
			byPath[effect.Path] = effect
		}
		return byPath
	}
	foreignRows, ownRows := read(false), read(true)
	for _, shape := range shapes {
		foreignRow, inForeign := foreignRows[shape.path]
		ownRow, inOwn := ownRows[shape.path]
		if inForeign != (shape.want == foreign) || inOwn != (shape.want == own) {
			t.Errorf("%s: in foreign %v, in own %v, want class %d", shape.path, inForeign, inOwn, shape.want)
		}
		if inOwn && (!ownRow.DuringCommandOf("s1") || ownRow.ActorClassFor("s1") == sourceledger.ActorYou || ownRow.BranchID.IsWorker()) {
			t.Errorf("%s: own class row %+v disagrees with the effect's predicates", shape.path, ownRow)
		}
		if inForeign && (foreignRow.DuringCommandOf("s1") || foreignRow.ActorClassFor("s1") == sourceledger.ActorYou || foreignRow.BranchID.IsWorker()) {
			t.Errorf("%s: foreign class row %+v disagrees with the effect's predicates", shape.path, foreignRow)
		}
	}
}
