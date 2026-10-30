package survey

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestCredentialDeliveryCoversFinalSearchAndSummaryText(t *testing.T) {
	_, tc, recorder := seedCredentialRead(t, "TOKEN=person-value-4411")
	tc.Identity.Agent = "implement"
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{{ID: "implement", Tools: map[string]bool{"grep": true, "summarize": true, "diff": true}}})
	reads := projectpaths.NewReadSession(boundary, tc)
	defer reads.Close()
	search := &grepSearch{reads: reads, tctx: tc}
	testutil.FailErr(t, "deliver grep matches", deliverGrepCredentials(t.Context(), search, grepResponse{Matches: []grepMatch{{Path: ".env", Content: "TOKEN=person-value-4411"}}, Highlights: []readHighlight{{Path: ".env", Content: "TOKEN=person-value-4411"}}}))
	result := summarize.Result{Pack: summarize.ContextPack{Substance: []summarize.PackWindow{{Path: ".env", Body: "TOKEN=person-value-4411"}}}, Anchors: []summarize.Anchor{{Path: ".env", Excerpt: "TOKEN=person-value-4411"}}}
	testutil.FailErr(t, "deliver summary text", deliverSummaryCredentials(t.Context(), reads, tc, result))
	if len(recorder.reads) != 4 {
		t.Fatalf("deliveries=%+v", recorder.reads)
	}
	for _, read := range recorder.reads {
		if read.Path != ".env" || read.Content == "" {
			t.Fatalf("delivery=%+v", read)
		}
	}
	recorder.err = errors.New("exposure unavailable")
	if err := deliverGrepCredentials(t.Context(), search, grepResponse{Matches: []grepMatch{{Path: ".env", Content: "value"}}}); !errors.Is(err, recorder.err) {
		t.Fatalf("delivery error=%v", err)
	}
	if err := deliverSummaryCredentials(t.Context(), reads, tc, result); !errors.Is(err, recorder.err) {
		t.Fatalf("summary error=%v", err)
	}
}
