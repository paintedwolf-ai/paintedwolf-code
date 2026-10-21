package oar

import (
	"context"
	"encoding/json"
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
		assertProviderJSONExamples(t, tc.diagnostic, res.Decision.Copy["fix"])
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

func assertProviderJSONExamples(t *testing.T, diagnostic, fix string) {
	t.Helper()
	fragments := strings.Split(fix, "`")
	if diagnostic == "unescaped_newline" {
		if len(fragments) < 5 {
			t.Fatalf("missing newline examples: %s", fix)
		}
		var example map[string]string
		testutil.FailErr(t, "decode newline example", json.Unmarshal([]byte(fragments[3]), &example))
		if example["content"] != "first line\nsecond line\n" {
			t.Fatalf("newline example decodes to %q", example["content"])
		}
	}
	if diagnostic == "invalid_escape" {
		// The final Unicode prefix needs four digits before it is a complete escape.
		want := []string{"\\", "\"", "\\", "/", "\b", "\f", "\n", "\r", "\t"}
		if len(fragments) != 21 {
			t.Fatalf("missing escape examples: %s", fix)
		}
		for i, decoded := range want {
			var got string
			testutil.FailErr(t, "decode escape example", json.Unmarshal([]byte("\""+fragments[2*i+1]+"\""), &got))
			if got != decoded {
				t.Fatalf("escape %q decodes to %q, want %q", fragments[2*i+1], got, decoded)
			}
		}
		var unicode string
		testutil.FailErr(t, "decode Unicode example", json.Unmarshal([]byte("\""+fragments[19]+"0041\""), &unicode))
		if unicode != "A" {
			t.Fatalf("Unicode example decodes to %q", unicode)
		}
	}
}
