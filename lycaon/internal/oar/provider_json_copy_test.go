package oar

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProviderJSONRecoveryCopyIsBounded(t *testing.T) {
	ensureCatalog(t)
	l, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	rs, err := l.LoadDir(hintsDir(t))
	testutil.FailErr(t, "load", err)
	p := NewGuardPipeline(rs, l, NewCounterStore())
	p.EnableAnchor(AnchorContentOutput)
	for _, tc := range []struct{ diagnostic, cause string }{
		{"unescaped_newline", "literal newline"},
		{"unexpected_end", "before completing"},
		{"invalid_escape", "invalid escape"},
		{"", "not valid JSON"},
		{strings.Repeat("untrusted diagnostic ", 10000), "not valid JSON"},
	} {
		gc := NewGuardContext()
		gc.SetContentObservation(ContentObservation{ToolCallUnparsed: true, ToolCallDiagnostic: tc.diagnostic})
		res, err := p.EvaluateBlock(context.Background(), AnchorContentOutput, gc)
		testutil.FailErr(t, "evaluate", err)
		if !res.Enforced || res.Decision == nil || res.Decision.Code != "TOOL_CALL_UNPARSED" {
			t.Fatalf("missing parser rejection: %#v", res)
		}
		if !strings.Contains(res.Decision.Copy["cause"], tc.cause) {
			t.Fatalf("wrong parser cause: %#v", res.Decision.Copy)
		}
		bytes := 0
		for _, field := range res.Decision.Copy {
			bytes += len(field)
			if strings.Contains(field, "untrusted diagnostic") {
				t.Fatal("provider text leaked into recovery copy")
			}
		}
		if bytes > 900 {
			t.Fatalf("parser copy exceeds 900 bytes: %d", bytes)
		}
	}
}
