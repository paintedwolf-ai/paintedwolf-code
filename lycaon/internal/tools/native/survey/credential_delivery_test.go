package survey

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil/exposure"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// Summarize text drawn from a credential file is recorded as a delivery before
// it is returned, and a failed record withholds the result.
func TestSummarizeRecordsCredentialDelivery(t *testing.T) {
	const canary = "fixture-canary-credential-7f3a"
	dir := t.TempDir()
	writeFile(t, dir, ".env", "API_TOKEN="+canary+"\n")
	tool := testSummarizeTool(t, dir)

	recorder := &deliveredRecorder{}
	tctx := nativefixture.Context(dir)
	tctx.Effects.CredentialFiles = recorder
	raw, err := tool.Run(context.Background(), map[string]any{"path": ".env"}, tctx)
	if err != nil {
		t.Fatalf("summarize credential file: %v", err)
	}
	exposure.Present(t, raw, canary)
	delivered := false
	for _, read := range recorder.reads {
		if read.Path == ".env" && len(exposure.NewScanner(canary).Found(read.Content)) > 0 {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("returned credential text without a delivery record: %+v", recorder.reads)
	}

	refused := errors.New("fixture delivery refusal")
	failing := &deliveredRecorder{err: refused}
	tctx.Effects.CredentialFiles = failing
	raw, err = tool.Run(context.Background(), map[string]any{"path": ".env"}, tctx)
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the delivery refusal", err)
	}
	exposure.Absent(t, raw, canary)
}
