package sourceledger

import (
	"bytes"
	"fmt"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestRewindPlanIncludesTheWholeSuffixAndRetainsTurnIdentity(t *testing.T) {
	s, ctx := openLedger(t)
	testdbseed.InsertSession(t, s.sqlDB, "s1", "p1")
	insertWalkMessage(t, s, "first", "s1", "", "transcript", "first", 1)
	mustRecord(t, s, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "first.txt"}, ProjectID: "p1", SessionID: "s1", Turn: 1, Origin: api.SourceChangeOriginAgent, Op: api.SourceChangeOpCreate, After: []byte("first")})
	insertWalkMessage(t, s, "second", "s1", "", "transcript", "second", 2)
	mustRecord(t, s, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "second.txt"}, ProjectID: "p1", SessionID: "s1", Turn: 2, Origin: api.SourceChangeOriginAgent, Op: api.SourceChangeOpCreate, After: []byte("second")})
	plan, err := s.Comparisons.PlanRewind(ctx, "p1", "s1", []string{"first", "second"})
	testutil.FailErr(t, "plan suffix", err)
	if len(plan.Files) != 2 || len(plan.Issues) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	for _, f := range plan.Files {
		if f.Target.State != "absent" {
			t.Fatalf("target = %+v", f.Target)
		}
	}
	_, err = s.sqlDB.ExecContext(ctx, `DELETE FROM messages WHERE session_id='s1'`)
	testutil.FailErr(t, "remove transcript", err)
	insertWalkMessage(t, s, "replacement", "s1", "", "transcript", "reply only", 3)
	summaries, err := s.Walk.WalkSummary(ctx, "p1", "s1", []string{"replacement"})
	testutil.FailErr(t, "read new turn attribution", err)
	if len(summaries) != 1 || summaries[0].Steps != 0 {
		t.Fatalf("discarded effects leaked into new turn: %+v", summaries)
	}
	var turn int
	err = s.sqlDB.QueryRowContext(ctx, `SELECT turn FROM session_source_turns WHERE opening_message_id='replacement'`).Scan(&turn)
	testutil.FailErr(t, "read allocated turn", err)
	if turn != 3 {
		t.Fatalf("turn=%d, want 3", turn)
	}
}

func TestRewindPlanRejectsLaterAndInterveningContributions(t *testing.T) {
	for _, between := range []bool{false, true} {
		name := "later"
		if between {
			name = "intervening"
		}
		t.Run(name, func(t *testing.T) {
			s, ctx := openLedger(t)
			testdbseed.InsertSession(t, s.sqlDB, "s1", "p1")
			insertWalkMessage(t, s, "first", "s1", "", "transcript", "first", 1)
			mustRecord(t, s, ctx, RecordInput{
				RecordLocation: RecordLocation{RootID: "r1", Path: "file"}, ProjectID: "p1", SessionID: "s1", Turn: 1, Origin: api.SourceChangeOriginAgent, Op: api.SourceChangeOpWrite, Before: []byte("base"), After: []byte("agent")})
			mustRecord(t, s, ctx, RecordInput{
				RecordLocation: RecordLocation{RootID: "r1", Path: "file"}, ProjectID: "p1", Origin: api.SourceChangeOriginExternal, Op: api.SourceChangeOpWrite, Before: []byte("agent"), After: []byte("human")})
			anchors := []string{"first"}
			if between {
				insertWalkMessage(t, s, "second", "s1", "", "transcript", "second", 2)
				anchors = append(anchors, "second")
				mustRecord(t, s, ctx, RecordInput{
					RecordLocation: RecordLocation{RootID: "r1", Path: "file"}, ProjectID: "p1", SessionID: "s1", Turn: 2, Origin: api.SourceChangeOriginAgent, Op: api.SourceChangeOpWrite, Before: []byte("human"), After: []byte("agent again")})
			}
			plan, err := s.Comparisons.PlanRewind(ctx, "p1", "s1", anchors)
			testutil.FailErr(t, "plan conflicting suffix", err)
			if len(plan.Issues) == 0 {
				t.Fatal("rewind would discard independent contribution")
			}
		})
	}
}

func TestRewindDoesNotTreatUserSessionContextAsAgentAuthorship(t *testing.T) {
	s, ctx := openLedger(t)
	testdbseed.InsertSession(t, s.sqlDB, "s1", "p1")
	insertWalkMessage(t, s, "first", "s1", "", "transcript", "first", 1)
	mustRecord(t, s, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "human.txt"}, ProjectID: "p1", SessionID: "s1", Turn: 1, Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpCreate, After: []byte("human")})
	plan, err := s.Comparisons.PlanRewind(ctx, "p1", "s1", []string{"first"})
	testutil.FailErr(t, "plan user-only source", err)
	if len(plan.Files) != 0 || len(plan.Issues) != 0 {
		t.Fatalf("user edit selected: %+v", plan)
	}
}

func TestRewindPlanChecksTheFinalEffectByteBudget(t *testing.T) {
	s, ctx := openLedger(t)
	testdbseed.InsertSession(t, s.sqlDB, "s1", "p1")
	insertWalkMessage(t, s, "first", "s1", "", "transcript", "first", 1)
	before := bytes.Repeat([]byte("a"), MaxRevisionContentBytes)
	for i := range 9 {
		after := bytes.Repeat([]byte{byte('b' + i)}, MaxRevisionContentBytes)
		mustRecord(t, s, ctx, RecordInput{
			RecordLocation: RecordLocation{RootID: "r1", Path: "file"}, ProjectID: "p1", SessionID: "s1", Turn: 1, Origin: api.SourceChangeOriginAgent, Op: api.SourceChangeOpWrite, Before: before, After: after})
		if i >= 7 {
			plan, err := s.Comparisons.PlanRewind(ctx, "p1", "s1", []string{"first"})
			testutil.FailErr(t, "plan byte boundary", err)
			hasLimit := false
			for _, issue := range plan.Issues {
				hasLimit = hasLimit || issue.Code == "history_limit"
			}
			if hasLimit != (i == 8) {
				t.Fatalf("effect %d: limit=%v issues=%+v", i+1, hasLimit, plan.Issues)
			}
		}
		before = after
	}
}

func TestRewindPlanFileLimitCountsLogicalFiles(t *testing.T) {
	s, ctx := openLedger(t)
	testdbseed.InsertSession(t, s.sqlDB, "s1", "p1")
	insertWalkMessage(t, s, "first", "s1", "", "transcript", "first", 1)
	for i := range 500 {
		mustRecord(t, s, ctx, RecordInput{
			RecordLocation: RecordLocation{RootID: "r1", Path: fmt.Sprintf("file-%d", i)}, ProjectID: "p1", SessionID: "s1", Turn: 1, Origin: api.SourceChangeOriginAgent, Op: api.SourceChangeOpCreate, After: []byte("created")})
	}
	mustRecord(t, s, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "file-0"}, ProjectID: "p1", SessionID: "s1", Turn: 1, Origin: api.SourceChangeOriginAgent, Op: api.SourceChangeOpWrite, Before: []byte("created"), After: []byte("updated")})
	plan, err := s.Comparisons.PlanRewind(ctx, "p1", "s1", []string{"first"})
	testutil.FailErr(t, "plan repeated file at limit", err)
	if len(plan.Files) != 500 || len(plan.Issues) != 0 || string(plan.Files[0].Expected.Content) != "updated" {
		t.Fatalf("files=%d issues=%+v", len(plan.Files), plan.Issues)
	}
	mustRecord(t, s, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "overflow"}, ProjectID: "p1", SessionID: "s1", Turn: 1, Origin: api.SourceChangeOriginAgent, Op: api.SourceChangeOpCreate, After: []byte("overflow")})
	plan, err = s.Comparisons.PlanRewind(ctx, "p1", "s1", []string{"first"})
	testutil.FailErr(t, "plan file overflow", err)
	if len(plan.Issues) != 1 || plan.Issues[0].Code != "history_limit" {
		t.Fatalf("overflow issues=%+v", plan.Issues)
	}
}
