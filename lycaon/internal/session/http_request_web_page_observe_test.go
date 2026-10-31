package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHTTPRequestWebPageCardPointsAtFetchURL(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-web-page"}
	args := map[string]any{"url": "https://docs.example.test/guide"}
	raised := func(deferred bool) guidance.ToolResultFacts {
		return guidance.ToolResultFacts{}.WithFeedback(toolrejection.HTTPRequestWebPageCode, map[string]any{
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
	if !facts.HasCode(toolrejection.HTTPRequestWebPageCode) {
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

func TestHTTPRequestLoopbackOtherSessionCardAdvisesReportingToUser(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-loopback-other"}
	args := map[string]any{"url": "http://127.0.0.1:8000/"}
	detail := "Port 8000 is served by `python3 -m http.server 8000`, a background command in the chat 'Tic Tac Toe Game with AI' in the project 'Tic-Tac-Toe'."
	raised := guidance.ToolResultFacts{}.WithFeedback(toolrejection.HTTPRequestLoopbackOtherSessionCode, map[string]any{
		"destination": "127.0.0.1:8000",
		"port":        "8000",
		"detail":      detail,
	}, &api.FeedbackSubject{Kind: "host", ID: "127.0.0.1:8000"})

	out, facts := mgr.ToolPolicy.AfterTool(context.Background(), sess, "http_request", args, `{"status":200}`, 1, raised)
	for _, want := range []string{
		`{"status":200}`,
		"Code: HTTP_REQUEST_LOOPBACK_OTHER_SESSION",
		detail,
		"let the user decide",
		"Do not stop, signal, or reconfigure the other listener yourself",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if !facts.HasCode(toolrejection.HTTPRequestLoopbackOtherSessionCode) {
		t.Fatalf("facts = %#v, want %s", facts, toolrejection.HTTPRequestLoopbackOtherSessionCode)
	}
}
