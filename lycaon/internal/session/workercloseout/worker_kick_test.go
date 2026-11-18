package workercloseout_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
)

// Without a rendered kick, the worker sees only the host marker for the kick.
func TestWorkerKickMarkerFallbackIsMarkerOnly(t *testing.T) {
	data := map[string]any{"max_chars": 12000, "actual_chars": 21000}
	failing := func(context.Context, string, map[string]any) (string, error) {
		return "", errors.New("render unavailable")
	}
	for _, tc := range []struct {
		kick, want string
	}{
		{anchor.InformRender(anchor.WorkerCloseout), "[host:worker-closeout]"},
		{anchor.InformRender(anchor.WorkerCancelCloseout), "[host:worker-cancel-closeout]"},
		{anchor.InformRender(anchor.WorkerCitationGrounding), "[host:worker-citation-grounding]"},
	} {
		if got := workercloseout.RenderWorkerKick(context.Background(), nil, tc.kick, data); got != tc.want {
			t.Fatalf("kick %q with no renderer = %q, want %q", tc.kick, got, tc.want)
		}
		if got := workercloseout.RenderWorkerKick(context.Background(), failing, tc.kick, data); got != tc.want {
			t.Fatalf("kick %q with a failing renderer = %q, want %q", tc.kick, got, tc.want)
		}
	}
	if got := workercloseout.RenderWorkerKick(context.Background(), nil, "not-an-anchor", data); got != "" {
		t.Fatalf("unknown kick = %q, want no text", got)
	}
}
