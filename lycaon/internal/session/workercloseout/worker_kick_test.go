package workercloseout_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
)

func TestWorkerKickMarkerFallbackCloseout(t *testing.T) {
	got := workercloseout.RenderWorkerKick(context.Background(), nil, anchor.InformRender(anchor.WorkerCloseout), nil)
	if got != "[host:worker-closeout]" {
		t.Fatalf("got = %q", got)
	}
}
