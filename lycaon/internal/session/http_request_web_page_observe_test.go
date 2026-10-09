package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHTTPRequestWebPageCardPointsAtFetchURL(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-web-page"}
	args := map[string]any{"url": "https://docs.example.test/guide"}
	raised := func(deferred bool) guidance.ToolResultFacts {
		return guidance.ToolResultFacts{}.WithFeedback(tools.HTTPRequestWebPageCode, map[string]any{
			"host":               "docs.example.test",
			"bytes":              "612480",
			"content_type":       "text/html; charset=utf-8",
			"fetch_url_deferred": deferred,
		}, &api.FeedbackSubject{Kind: "host", ID: "docs.example.test"})
	}

	out, facts := mgr.ToolPolicy.AfterTool(context.Background(), sess, "http_request", args, `{"status":200}`, 1, raised(false))
	for _, want := range []string{
		`{"status":200}`,
		"Code: HTTP_REQUEST_WEB_PAGE",
		"612480-byte HTML document from docs.example.test",
		"call fetch_url with the same URL.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if !facts.HasCode(tools.HTTPRequestWebPageCode) {
		t.Fatalf("facts = %#v, want the web page code", facts)
	}

	deferred, _ := mgr.ToolPolicy.AfterTool(context.Background(), sess, "http_request", args, `{"status":200}`, 1, raised(true))
	if !strings.Contains(deferred, "after loading it with request_tools") {
		t.Fatalf("deferred fetch_url card omits request_tools:\n%s", deferred)
	}

	plain, _ := mgr.ToolPolicy.AfterTool(context.Background(), sess, "http_request", args, `{"status":200}`, 1, guidance.ToolResultFacts{})
	if strings.Contains(plain, "HTTP_REQUEST_WEB_PAGE") {
		t.Fatalf("an exchange with no stated web page carried the card:\n%s", plain)
	}
}
