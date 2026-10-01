package approvaloutcome_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/testutil"
)

func bundledCatalog(t *testing.T) *approvaloutcome.Catalog {
	t.Helper()
	cfg, err := approvaloutcome.Load()
	testutil.FailErr(t, "load approval-outcome catalog", err)
	return approvaloutcome.NewCatalog(cfg)
}

// TestBundledCatalogRendersScenarios guards that every code's authored copy renders
// and carries the substrings its scenarios promise.
func TestBundledCatalogRendersScenarios(t *testing.T) {
	cfg, err := approvaloutcome.Load()
	testutil.FailErr(t, "load approval-outcome catalog", err)
	cat := approvaloutcome.NewCatalog(cfg)

	for code, entry := range cfg.Outcomes {
		for _, sc := range entry.Scenarios {
			msg := cat.Message(code, sc.Vars)
			if strings.TrimSpace(msg) == "" {
				t.Fatalf("%s/%s: empty render", code, sc.ID)
			}
			for _, want := range sc.ExpectContains {
				if !strings.Contains(msg, want) {
					t.Fatalf("%s/%s: render %q missing %q", code, sc.ID, msg, want)
				}
			}
		}
	}
}

func TestDeniedAndExpiredAreDistinct(t *testing.T) {
	cat := bundledCatalog(t)
	denied := cat.Message(approvaloutcome.CodeApprovalDenied, nil)
	expired := cat.Message(approvaloutcome.CodeApprovalExpired, nil)
	if denied == expired {
		t.Fatalf("denied and expired copy must differ: %q", denied)
	}
	if !strings.Contains(expired, "not a denial") {
		t.Fatalf("expired copy must name itself a timeout, got %q", expired)
	}
}

func TestUnknownCodeFallsBackToCode(t *testing.T) {
	cat := bundledCatalog(t)
	if got := cat.Message("no_such_code", nil); got != "no_such_code" {
		t.Fatalf("unknown code = %q want identifier passthrough", got)
	}
}

func TestRenderFailureFallsBackToStableCodeNotTemplateSource(t *testing.T) {
	cfg := &approvaloutcome.Config{Outcomes: map[string]approvaloutcome.Entry{
		"broken": {Message: "{{ invalid"},
	}}
	cat := approvaloutcome.NewCatalog(cfg)
	if got := cat.Message("broken", nil); got != "broken" {
		t.Fatalf("broken template fallback = %q", got)
	}
}
