package httpaction

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolprofiles"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/tools"
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

func TestRemotePageClassifiesSuccessfulRemoteHTMLGets(t *testing.T) {
	loopbackTarget, err := url.Parse("http://127.0.0.1:8080/")
	if err != nil {
		t.Fatalf("parse loopback: %v", err)
	}
	cases := []struct {
		name string
		spec func(requestSpec) requestSpec
		resp func(outboundhttp.Response) outboundhttp.Response
		want bool
	}{
		{name: "html", want: true},
		{name: "xhtml", resp: func(r outboundhttp.Response) outboundhttp.Response {
			r.ContentType = "application/xhtml+xml"
			return r
		}, want: true},
		{name: "json", resp: func(r outboundhttp.Response) outboundhttp.Response {
			r.ContentType = "application/json"
			return r
		}},
		{name: "error page", resp: func(r outboundhttp.Response) outboundhttp.Response {
			r.Status = http.StatusNotFound
			return r
		}},
		{name: "post", spec: func(s requestSpec) requestSpec {
			s.method = "POST"
			return s
		}},
		{name: "loopback service", spec: func(s requestSpec) requestSpec {
			s.target = loopbackTarget
			return s
		}},
		{name: "unix socket", spec: func(s requestSpec) requestSpec {
			s.socket = "/tmp/app.sock"
			return s
		}},
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
			if got := remotePage(spec, resp); got != tc.want {
				t.Fatalf("remotePage = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoopbackPageIsNotARemotePage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><h1>Dashboard</h1></body></html>`))
	}))
	t.Cleanup(server.Close)

	ctx, scope := socketref.WithScope(t.Context())
	defer scope.Close()
	ex, err := Perform(ctx, Deps{Boundary: testBoundary()}, map[string]any{
		"url":                server.URL,
		"capability_request": loopbackCapability(t, server.URL),
	}, tools.ToolContext{Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID}}, ExchangeOptions{})
	testutil.FailErr(t, "perform", err)
	if ex.RemotePage {
		t.Fatal("a page from a local service under test counted as a remote page")
	}
}
