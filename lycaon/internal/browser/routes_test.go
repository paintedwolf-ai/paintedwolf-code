package browser

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	testutil.FailErr(t, "url.Parse failed", err)
	return u
}

func TestRouteTableMatchesPathsFullURLsAndMethodsInOrder(t *testing.T) {
	table := newRouteTable([]RouteRule{
		{URL: "/api/items", Method: "POST", Status: 201},
		{URL: "/api/items*", Body: `[]`},
		{URL: "https://api.example.test/v1/*", Fail: "connection_refused"},
	})
	cases := []struct {
		method, url string
		index       int
		matched     bool
	}{
		{"POST", "http://127.0.0.1:3000/api/items", 0, true},
		{"GET", "http://127.0.0.1:3000/api/items", 1, true},
		{"GET", "http://127.0.0.1:3000/api/items?page=2", 1, true},
		{"GET", "https://api.example.test/v1/charges", 2, true},
		{"GET", "http://127.0.0.1:3000/api/other", 0, false},
	}
	for _, tc := range cases {
		_, index, ok := table.match(tc.method, mustURL(t, tc.url))
		if ok != tc.matched || (ok && index != tc.index) {
			t.Fatalf("%s %s matched=%v index=%d; want %v %d", tc.method, tc.url, ok, index, tc.matched, tc.index)
		}
	}
}

func TestRouteTimesAnswersOnlyThatManyRequests(t *testing.T) {
	table := newRouteTable([]RouteRule{{URL: "/api/flaky", Times: 1, Fail: "timed_out"}})
	u := mustURL(t, "http://127.0.0.1/api/flaky")
	if _, _, ok := table.match("GET", u); !ok {
		t.Fatal("first request was not answered by the route")
	}
	if _, _, ok := table.match("GET", u); ok {
		t.Fatal("second request was answered after times ran out")
	}
	table.replace([]RouteRule{{URL: "/api/flaky", Times: 1, Fail: "timed_out"}})
	if _, _, ok := table.match("GET", u); !ok {
		t.Fatal("replacing the table did not reset its counts")
	}
}

func TestRouteAnswersDescribeExactlyOneResponse(t *testing.T) {
	passThrough := routeAnswer(RouteRule{URL: "/slow", DelayMS: 250}, 0)
	if !passThrough.pass || passThrough.delay.Milliseconds() != 250 || passThrough.servedBy != networkServedByRoute {
		t.Fatalf("delay-only route = %+v, want a delayed pass-through", passThrough)
	}
	failed := routeAnswer(RouteRule{URL: "/x", Fail: "connection_refused"}, 1)
	if failed.fail != proto.NetworkErrorReasonConnectionRefused || *failed.route != 1 {
		t.Fatalf("failing route = %+v", failed)
	}
	body := routeAnswer(RouteRule{URL: "/x", Body: `{"ok":true}`, Headers: map[string]string{"x-trace": "1"}}, 2)
	if body.status != 200 || body.headers["Content-Type"] != defaultRouteMimeType || body.headers["X-Trace"] != "1" {
		t.Fatalf("body route = %+v", body)
	}
	typed := routeAnswer(RouteRule{URL: "/x", Status: 503, BodyBytes: []byte("down"), ContentType: "text/plain"}, 3)
	if typed.status != 503 || string(typed.body) != "down" || typed.headers["Content-Type"] != "text/plain" {
		t.Fatalf("typed route = %+v", typed)
	}
}

func TestValidateRoutesNamesTheBrokenRule(t *testing.T) {
	cases := map[string][]RouteRule{
		"missing_url":        {{Status: 200}},
		"url_not_absolute":   {{URL: "api/items"}},
		"bad_status":         {{URL: "/x", Status: 42}},
		"unknown_failure":    {{URL: "/x", Fail: "gremlins"}},
		"fail_with_response": {{URL: "/x", Fail: "failed", Status: 500}},
		"body_and_body_path": {{URL: "/x", Body: "a", BodyPath: "b.json"}},
		"bad_delay":          {{URL: "/x", DelayMS: MaxRouteDelayMS + 1}},
		"body_too_large":     {{URL: "/x", Body: strings.Repeat("x", MaxRouteBodyBytes+1)}},
	}
	for reason, rules := range cases {
		err := ValidateRoutes(rules)
		var rej *browserengine.RejectError
		if !errors.As(err, &rej) || rej.Code != "CAPTURE_ROUTE_INVALID" || rej.Data["reason"] != reason {
			t.Fatalf("%s: got %v", reason, err)
		}
	}
	if err := ValidateRoutes(make([]RouteRule, MaxRoutes+1)); err == nil {
		t.Fatal("an oversized route table was accepted")
	}
	if err := ValidateRoutes([]RouteRule{{URL: "*", DelayMS: 10}, {URL: "/api/*", Body: "{}"}}); err != nil {
		t.Fatalf("valid routes rejected: %v", err)
	}
}
