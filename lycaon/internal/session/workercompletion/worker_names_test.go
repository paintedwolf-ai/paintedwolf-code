package workercompletion

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The completion envelope and digest carry the worker's name and keep the job
// id as host identity; a round with no worker names nothing.
func TestEnvelopeNamesTheWorker(t *testing.T) {
	out := FormatWorkerCompletionEnvelope(WorkerCompletionEnvelope{Worker: "parser-fix", JobID: "job-1", AgentType: "implementer", State: "complete"})
	env, ok := ParseWorkerCompletionEnvelope(out)
	if !ok || env.Worker != "parser-fix" || env.JobID != "job-1" {
		t.Fatalf("round trip = %+v ok=%v\n%s", env, ok, out)
	}
	if digest := FormatWorkerDigest("implementer", "parser-fix", "complete", "", WorkerCompletionProof{}, WorkerCompletionReport{}); !strings.Contains(digest, "parser-fix") {
		t.Fatalf("digest = %q", digest)
	}
	out = FormatWorkerCompletionEnvelope(WorkerCompletionEnvelope{JobID: "job-2", State: "complete"})
	if env, _ := ParseWorkerCompletionEnvelope(out); env.Worker != "" {
		t.Fatalf("unnamed round = %+v", env)
	}
}

// Name returns the worker's name, or nothing for a job with no worker, a
// failed lookup, or no lookup at all.
func TestWorkerNamesName(t *testing.T) {
	names := WorkerNames(func(_ context.Context, jobID string) (string, bool, error) {
		switch jobID {
		case "job-1":
			return "parser-fix", true, nil
		case "broken":
			return "", false, errors.New("store closed")
		}
		return "", false, nil
	})
	for job, want := range map[string]string{"job-1": "parser-fix", "job-none": "", "broken": "", "": ""} {
		if got := names.Name(t.Context(), job); got != want {
			t.Fatalf("%q: name = %q want %q", job, got, want)
		}
	}
	if got := WorkerNames(nil).Name(t.Context(), "job-1"); got != "" {
		t.Fatalf("nil names = %q", got)
	}
}
