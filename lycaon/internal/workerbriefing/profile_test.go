package workerbriefing

import (
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

func (w workers) Get(id string) (*wire.WorkerTask, bool) { task, ok := w[id]; return task, ok }

var finished = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

func job() *wire.WorkerTask {
	return &wire.WorkerTask{ID: "job", ProjectID: "p", Status: "completed", Brief: "SUPPLIED_BRIEF", CompletedAt: &finished,
		Result: &wire.WorkerResult{Summary: "SUPPLIED_CLAIM",
			ChangeReport: &wire.WorkerChangeReport{ChangedPaths: []string{"auth/token.go"}, DiffStat: []wire.WorkerDiffStat{{Path: "auth/token.go", Insertions: 4}}}}}
}

func target(jobID, revision string) briefing.Target {
	raw, _ := json.Marshal(wire.WorkerResultBriefingTarget{JobID: jobID, ResultRevision: revision})
	return briefing.Target{ProjectID: "p", Ref: raw}
}

func TestWorkerSummaryIsOneCallKeepingClaimsApartFromReceipts(t *testing.T) {
	request, err := New(workers{"job": job()}).Resolve(t.Context(), target("job", finished.In(time.FixedZone("x", 3600)).Format(time.RFC3339Nano)))
	testutil.FailErr(t, "resolve", err)
	if len(request.Sections) != 1 || request.Sections[0].Mode != briefing.Generated || len(request.Sections[0].Manifest.Contract.Labels) != 3 {
		t.Fatalf("sections %+v", request.Sections)
	}
	user := request.Sections[0].Prompt.User
	claim, receipt := strings.Index(user, "SUPPLIED_CLAIM"), strings.Index(user, "auth/token.go +4 -0")
	if claim < 0 || receipt < 0 || !strings.Contains(user, "SUPPLIED_BRIEF") {
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
	profile := New(workers{"job": job(), "running": running, "other": other})
	cases := map[string]struct {
		target briefing.Target
		want   error
	}{
		"unfinished":      {target("running", finished.Format(time.RFC3339Nano)), briefing.ErrTargetUnavailable},
		"another project": {target("other", finished.Format(time.RFC3339Nano)), briefing.ErrTargetUnavailable},
		"stale revision":  {target("job", finished.Add(time.Second).Format(time.RFC3339Nano)), briefing.ErrTargetUnavailable},
		"unknown worker":  {target("missing", finished.Format(time.RFC3339Nano)), briefing.ErrTargetUnavailable},
		"malformed":       {target("job", "yesterday"), briefing.ErrInvalidTarget},
	}
	for name, c := range cases {
		if _, err := profile.Resolve(t.Context(), c.target); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
