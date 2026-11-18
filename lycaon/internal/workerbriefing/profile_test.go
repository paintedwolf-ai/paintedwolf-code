package workerbriefing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type workers map[string]*wire.WorkerTask

func (w workers) LookupByResult(_ context.Context, handle string) (*wire.WorkerTask, bool) {
	task, ok := w[handle]
	return task, ok
}

// finals answers final-output reads keyed by session and job.
type finals map[[2]string]briefing.WorkerFinalRead

func (f finals) ReadWorkerFinal(_ context.Context, sessionID, workerJobID string) (briefing.WorkerFinalRead, error) {
	return f[[2]string{sessionID, workerJobID}], nil
}

const (
	resultHandle1 = "01900000-0000-7000-8000-000000000001"
	resultHandle2 = "01900000-0000-7000-8000-000000000002"
	runningHandle = "01900000-0000-7000-8000-000000000003"
	otherHandle   = "01900000-0000-7000-8000-000000000004"
	missingHandle = "01900000-0000-7000-8000-000000000005"
)

var finished = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

func job() *wire.WorkerTask {
	return &wire.WorkerTask{
		ID: "job", WorkerName: "fixer", Round: 2, ResultHandle: resultHandle1,
		ResultDisposition: wire.WorkerResultOpen,
		ProjectID:         "p", Status: "complete", Brief: "SUPPLIED_BRIEF", CompletedAt: &finished,
		Result: &wire.WorkerResult{Summary: "SUPPLIED_CLAIM",
			ChangeReport: &wire.WorkerChangeReport{ChangedPaths: []string{"auth/token.go"}, DiffStat: []wire.WorkerDiffStat{{Path: "auth/token.go", Insertions: 4}}}},
	}
}

func target(handle string) briefing.Target {
	raw, _ := json.Marshal(wire.WorkerResultBriefingTarget{ResultHandle: handle})
	return briefing.Target{ProjectID: "p", Ref: raw}
}

func TestWorkerSummaryIsOneCallKeepingClaimsApartFromReceipts(t *testing.T) {
	second := job()
	second.Round = 3
	second.ResultHandle = resultHandle2
	store := workers{resultHandle1: job(), resultHandle2: second}
	request, err := New(store, finals{}).Resolve(t.Context(), target(resultHandle1))
	testutil.FailErr(t, "resolve", err)
	if request.TargetKey != resultHandle1 || request.ScopeFingerprint != resultHandle1 {
		t.Fatalf("target key = %q fingerprint = %q, want %q", request.TargetKey, request.ScopeFingerprint, resultHandle1)
	}
	later, err := New(store, finals{}).Resolve(t.Context(), target(resultHandle2))
	testutil.FailErr(t, "resolve later round", err)
	if later.TargetKey == request.TargetKey {
		t.Fatalf("distinct result handles shared target key %q", later.TargetKey)
	}
	if len(request.Sections) != 1 || request.Sections[0].Mode != briefing.Generated || len(request.Sections[0].Manifest.Contract.Labels) != 3 {
		t.Fatalf("sections %+v", request.Sections)
	}
	user := request.Sections[0].Prompt.User
	claim, receipt := strings.Index(user, "SUPPLIED_CLAIM"), strings.Index(user, "auth/token.go +4 -0")
	if claim < 0 || receipt < 0 || !strings.Contains(user, "SUPPLIED_BRIEF") || !strings.Contains(user, resultHandle1) {
		t.Fatalf("prompt lost evidence: %s", user)
	}
	roles := map[string]briefing.Role{}
	for _, item := range request.Sections[0].Manifest.Items {
		roles[item.Key] = item.Role
	}
	if roles["report"] == roles["receipts"] {
		t.Fatal("the worker's claims and host receipts share a role")
	}
}

func TestWorkerSummaryRefusals(t *testing.T) {
	running := job()
	running.CompletedAt = nil
	other := job()
	other.ProjectID = "elsewhere"
	profile := New(workers{resultHandle1: job(), runningHandle: running, otherHandle: other}, finals{})
	cases := map[string]struct {
		target briefing.Target
		want   error
	}{
		"unfinished":      {target(runningHandle), briefing.ErrTargetUnavailable},
		"another project": {target(otherHandle), briefing.ErrTargetUnavailable},
		"unknown result":  {target(missingHandle), briefing.ErrTargetUnavailable},
		"empty":           {target(""), briefing.ErrInvalidTarget},
		"malformed":       {target("not-a-uuid"), briefing.ErrInvalidTarget},
	}
	for name, c := range cases {
		if _, err := profile.Resolve(t.Context(), c.target); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// Only a round that failed or was canceled reads its last reasoning, from its
// own session and job.
func TestStoppedRoundsCarryTheirLastReasoning(t *testing.T) {
	thinking := briefing.ReasoningRead{Present: true, TextChars: 26, Tail: "SUPPLIED_REASONING_IN_HAND"}
	reads := finals{
		{"child", "job"}:   {Found: true, OutputID: "final", Reasoning: thinking},
		{"parent", "job"}:  {Found: true, OutputID: "parent-row", Reasoning: briefing.ReasoningRead{Present: true, TextChars: 5, Tail: "WRONG"}},
		{"child", "other"}: {Found: true, OutputID: "other-row", Reasoning: briefing.ReasoningRead{Present: true, TextChars: 5, Tail: "WRONG"}},
	}
	for _, status := range []wire.WorkerStatus{wire.WorkerStatusComplete, wire.WorkerStatusFailed, wire.WorkerStatusCanceled} {
		task := job()
		task.Status, task.ChildSessionID, task.ParentSessionID = status, "child", "parent"
		request, err := New(workers{resultHandle1: task}, reads).Resolve(t.Context(), target(resultHandle1))
		testutil.FailErr(t, "resolve "+string(status), err)
		section := request.Sections[0]
		var reasoning *briefing.Item
		for i := range section.Manifest.Items {
			if section.Manifest.Items[i].SourceKind == briefing.SourceReasoning {
				reasoning = &section.Manifest.Items[i]
			}
		}
		stopped := status != wire.WorkerStatusComplete
		if (reasoning != nil) != stopped {
			t.Fatalf("%s: reasoning item %+v", status, reasoning)
		}
		if !stopped {
			if len(section.Quality) != 0 || strings.Contains(section.Prompt.User, "SUPPLIED_REASONING") {
				t.Fatalf("%s: a completed round read reasoning", status)
			}
			continue
		}
		if reasoning.Text != thinking.Tail || reasoning.Ref != "message:final" || reasoning.Audience != "parent" {
			t.Fatalf("%s: item %+v", status, reasoning)
		}
		if !strings.Contains(section.Prompt.User, thinking.Tail) || len(section.Quality) != 1 || section.Quality[0] != briefing.QualityReasoningIncluded {
			t.Fatalf("%s: prompt or quality missed the reasoning: %v", status, section.Quality)
		}
		if _, err := section.Manifest.Hash(); err != nil {
			testutil.FailErr(t, "hash "+string(status), err)
		}
	}

	failed := job()
	failed.Status, failed.ChildSessionID = wire.WorkerStatusFailed, "silent"
	request, err := New(workers{resultHandle1: failed}, reads).Resolve(t.Context(), target(resultHandle1))
	testutil.FailErr(t, "resolve a round without output", err)
	items := request.Sections[0].Manifest.Items
	if last := items[len(items)-1]; last.SourceKind != briefing.SourceReasoning || last.Inclusion != briefing.Unavailable || len(request.Sections[0].Quality) != 0 {
		t.Fatalf("a round with no output: %+v quality %v", last, request.Sections[0].Quality)
	}
}
