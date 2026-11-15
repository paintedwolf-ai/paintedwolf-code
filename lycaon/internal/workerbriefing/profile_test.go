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
	request, err := New(store).Resolve(t.Context(), target(resultHandle1))
	testutil.FailErr(t, "resolve", err)
	if request.TargetKey != resultHandle1 || request.ScopeFingerprint != resultHandle1 {
		t.Fatalf("target key = %q fingerprint = %q, want %q", request.TargetKey, request.ScopeFingerprint, resultHandle1)
	}
	later, err := New(store).Resolve(t.Context(), target(resultHandle2))
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
	profile := New(workers{resultHandle1: job(), runningHandle: running, otherHandle: other})
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
