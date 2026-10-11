package httpaction

import (
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func remoteGET(t *testing.T) requestSpec {
	t.Helper()
	target, err := url.Parse("https://docs.example.test/guide")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	return requestSpec{method: "GET", target: target}
}

func htmlResponse() outboundhttp.Response {
	return outboundhttp.Response{Status: http.StatusOK, Bytes: 612480, ContentType: "text/html; charset=utf-8"}
}

func TestWebPageClassifiesDeliveredRemoteDocuments(t *testing.T) {
	inline := bodyPlacement{inline: "<html></html>", encoding: "utf-8"}
	spilled := bodyPlacement{omitted: true, encoding: "utf-8", spillPath: "tool-output/ab/cd"}
	loopbackTarget, err := url.Parse("http://127.0.0.1:8080/")
	if err != nil {
		t.Fatalf("parse loopback: %v", err)
	}
	cases := []struct {
		name      string
		spec      func(requestSpec) requestSpec
		resp      func(outboundhttp.Response) outboundhttp.Response
		placement bodyPlacement
		want      bool
	}{
		{name: "inline html", placement: inline, want: true},
		{name: "spilled html", placement: spilled, want: true},
		{name: "xhtml", resp: func(r outboundhttp.Response) outboundhttp.Response {
			r.ContentType = "application/xhtml+xml"
			return r
		}, placement: inline, want: true},
		{name: "landed or discarded", placement: bodyPlacement{omitted: true}},
		{name: "spill failed", placement: bodyPlacement{omitted: true, spillFailed: true}},
		{name: "json", resp: func(r outboundhttp.Response) outboundhttp.Response {
			r.ContentType = "application/json"
			return r
		}, placement: inline},
		{name: "error page", resp: func(r outboundhttp.Response) outboundhttp.Response {
			r.Status = http.StatusNotFound
			return r
		}, placement: inline},
		{name: "post", spec: func(s requestSpec) requestSpec {
			s.method = "POST"
			return s
		}, placement: inline},
		{name: "loopback service", spec: func(s requestSpec) requestSpec {
			s.target = loopbackTarget
			return s
		}, placement: inline},
		{name: "unix socket", spec: func(s requestSpec) requestSpec {
			s.socket = "/tmp/app.sock"
			return s
		}, placement: inline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, resp := remoteGET(t), htmlResponse()
			if tc.spec != nil {
				spec = tc.spec(spec)
			}
			if tc.resp != nil {
				resp = tc.resp(resp)
			}
			if got := webPage(spec, resp, tc.placement); got != tc.want {
				t.Fatalf("webPage = %v want %v", got, tc.want)
			}
		})
	}
}

func TestReportWebPageStatesFetchURLAvailability(t *testing.T) {
	inline := bodyPlacement{inline: "<html></html>", encoding: "utf-8"}
	cases := []struct {
		name         string
		plan         toolsurface.Plan
		wantRaised   bool
		wantDeferred bool
	}{
		{name: "immediate", plan: toolsurface.Compile([]string{"http_request", "fetch_url"}, nil), wantRaised: true},
		{name: "deferred", plan: toolsurface.Compile([]string{"http_request"}, []string{"fetch_url"}), wantRaised: true, wantDeferred: true},
		{name: "withheld", plan: toolsurface.Compile([]string{"http_request"}, nil)},
		{name: "no model surface"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tctx := tools.ToolContext{
				Turn:    tools.InvocationTurn{TurnToolPlan: tc.plan},
				Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
			}
			reportWebPage(tctx, remoteGET(t), htmlResponse(), inline, "https://www.example.test/guide/")
			if got := tctx.Effects.Out.Facts.HasCode(toolrejection.HTTPRequestWebPageCode); got != tc.wantRaised {
				t.Fatalf("raised = %v want %v: %+v", got, tc.wantRaised, tctx.Effects.Out.Facts)
			}
			if !tc.wantRaised {
				return
			}
			feedback := tctx.Effects.Out.Facts.FeedbackFor(toolrejection.HTTPRequestWebPageCode)
			if feedback.Details["host"] != "www.example.test" || feedback.Details["bytes"] != "612480" {
				t.Fatalf("details = %+v want the redirected host and byte count", feedback.Details)
			}
			if feedback.Details["fetch_url_deferred"] != tc.wantDeferred {
				t.Fatalf("fetch_url_deferred = %v want %v", feedback.Details["fetch_url_deferred"], tc.wantDeferred)
			}
		})
	}
}

func TestHTTPRequestToLoopbackPageStaysSilent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><h1>Dashboard</h1></body></html>`))
	}))
	t.Cleanup(server.Close)

	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}
	outcome := &tools.ToolInvocationOut{}
	if _, err := registry.Run(t.Context(), "http_request", map[string]any{
		"url":                server.URL,
		"capability_request": loopbackCapability(t, server.URL),
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
		Turn:     tools.InvocationTurn{TurnToolPlan: toolsurface.Compile([]string{"http_request", "fetch_url"}, nil)},
		Effects:  tools.InvocationEffects{Out: outcome},
	}); err != nil {
		t.Fatalf("run http_request: %v", err)
	}
	if outcome.Facts.HasCode(toolrejection.HTTPRequestWebPageCode) {
		t.Fatalf("a page from a local service under test raised %s", toolrejection.HTTPRequestWebPageCode)
	}
}
