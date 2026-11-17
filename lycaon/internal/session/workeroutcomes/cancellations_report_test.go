package workeroutcomes

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type capturedCards struct{ envelope string }

func (c *capturedCards) Project(_ context.Context, _, _ string, ws *api.WorkerSummaryMeta) error {
	c.envelope = ws.Envelope
	return nil
}

// A stop shows only a report the worker delivered; without one the host
// writes no report on the worker's behalf.
func TestStoppedRoundShowsOnlyADeliveredReport(t *testing.T) {
	cards := &capturedCards{}
	m := NewCancellations(CancellationPorts{Cards: cards})
	in := CancellationInput{JobID: "job-1", AgentType: "implementer", Reason: "stopped by the coordinator", Result: api.WorkerResult{Status: "canceled", Summary: "stopped"}}
	testutil.FailErr(t, "append without report", m.Append(t.Context(), "coord", in))
	if strings.Contains(cards.envelope, "report_json") || strings.Contains(cards.envelope, "Worker leg canceled") {
		t.Fatalf("report-less stop carries a report:\n%s", cards.envelope)
	}
	in.CompletionReport = workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "half the parser"}
	testutil.FailErr(t, "append with report", m.Append(t.Context(), "coord", in))
	if !strings.Contains(cards.envelope, "report_json") || !strings.Contains(cards.envelope, "half the parser") || !strings.Contains(cards.envelope, "partial") {
		t.Fatalf("delivered report missing or not partial:\n%s", cards.envelope)
	}
}
