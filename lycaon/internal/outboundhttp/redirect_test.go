package outboundhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// seenRequest records what one hop actually received.
type seenRequest struct {
	method      string
	body        string
	contentType string
	authorized  string
}

func redirectServer(t *testing.T, status int) (*httptest.Server, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, seenRequest{
			method: r.Method, body: string(body),
			contentType: r.Header.Get("Content-Type"), authorized: r.Header.Get("Authorization"),
		})
	}
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Location", "/done")
		w.WriteHeader(status)
	})
	mux.HandleFunc("/done", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte("landed"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &seen
}

func TestRedirectRewritesTheMethodTheWayBrowsersDo(t *testing.T) {
	cases := []struct {
		status     int
		method     string
		wantMethod string
		wantBody   string
	}{
		{http.StatusSeeOther, http.MethodPost, http.MethodGet, ""},
		{http.StatusSeeOther, http.MethodDelete, http.MethodGet, ""},
		{http.StatusMovedPermanently, http.MethodPost, http.MethodGet, ""},
		{http.StatusFound, http.MethodPost, http.MethodGet, ""},
		{http.StatusTemporaryRedirect, http.MethodPost, http.MethodPost, "payload"},
		{http.StatusPermanentRedirect, http.MethodPut, http.MethodPut, "payload"},
	}
	for _, tc := range cases {
		server, seen := redirectServer(t, tc.status)
		resp, err := Do(context.Background(), Request{
			Method: tc.method, URL: server.URL + "/submit", Body: []byte("payload"),
			OriginHeaders: []Header{{Name: "Content-Type", Value: "text/plain"}},
			Redirects:     RedirectSafe, AllowAddress: allowLoopback,
		})
		if err != nil {
			t.Fatalf("%d %s: %v", tc.status, tc.method, err)
		}
		if resp.Status != http.StatusOK || string(resp.Body) != "landed" {
			t.Fatalf("%d %s: status=%d body=%q", tc.status, tc.method, resp.Status, resp.Body)
		}
		if len(*seen) != 2 {
			t.Fatalf("%d %s: hops seen = %d", tc.status, tc.method, len(*seen))
		}
		final := (*seen)[1]
		if final.method != tc.wantMethod || final.body != tc.wantBody {
			t.Fatalf("%d %s: followed as %s with body %q, want %s / %q",
				tc.status, tc.method, final.method, final.body, tc.wantMethod, tc.wantBody)
		}
		if tc.wantBody == "" && final.contentType != "" {
			t.Fatalf("%d %s: content type %q survived a dropped body", tc.status, tc.method, final.contentType)
		}
	}
}

func TestRedirectHopNamesTheURLThatAnswered(t *testing.T) {
	server, _ := redirectServer(t, http.StatusSeeOther)
	resp, err := Do(context.Background(), Request{
		Method: http.MethodPost, URL: server.URL + "/submit", Body: []byte("payload"),
		Redirects: RedirectSafe, AllowAddress: allowLoopback,
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(resp.Redirects) != 1 {
		t.Fatalf("redirects = %+v", resp.Redirects)
	}
	hop := resp.Redirects[0]
	if hop.URL != server.URL+"/submit" || hop.Location != server.URL+"/done" ||
		hop.Status != http.StatusSeeOther || hop.Method != http.MethodGet {
		t.Fatalf("hop = %+v, want the answering URL, its status, its location, and the rewritten method", hop)
	}
}

func TestCrossOriginRedirectRefusesToResendTheBody(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the body reached a second origin")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(elsewhere.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", elsewhere.URL+"/take-it")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)

	_, err := Do(context.Background(), Request{
		Method: http.MethodPost, URL: origin.URL + "/submit", Body: []byte("secret-shaped payload"),
		Redirects: RedirectSafe, AllowAddress: allowLoopback,
	})
	var redirect *RedirectError
	if !errors.As(err, &redirect) || !strings.Contains(redirect.Reason, "another origin") {
		t.Fatalf("error = %v, want a refused cross-origin body re-send", err)
	}
	if !Permanent(err) {
		t.Fatal("a refused redirect chain was reported retryable")
	}
	if len(redirect.Hops) != 1 {
		t.Fatalf("hops = %+v, want the hop that forced the refusal", redirect.Hops)
	}
}

func TestCrossOriginRedirectWithoutABodyDropsCallerHeaders(t *testing.T) {
	var landed seenRequest
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		landed = seenRequest{method: r.Method, authorized: r.Header.Get("Authorization")}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(elsewhere.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", elsewhere.URL+"/next")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	if _, err := Do(context.Background(), Request{
		Method: http.MethodGet, URL: origin.URL + "/start",
		OriginHeaders: []Header{{Name: "Authorization", Value: "Bearer caller-token"}},
		Redirects:     RedirectSafe, AllowAddress: allowLoopback,
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if landed.authorized != "" {
		t.Fatalf("caller Authorization crossed origins: %q", landed.authorized)
	}
}

func TestHeadReportsNoBodyFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	resp, err := Do(context.Background(), Request{
		Method: http.MethodHead, URL: server.URL, AllowAddress: allowLoopback,
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.BodyObserved || resp.Bytes != 0 || resp.SHA256 != "" {
		t.Fatalf("HEAD reported body facts: observed=%v bytes=%d sha=%q", resp.BodyObserved, resp.Bytes, resp.SHA256)
	}
	if resp.ContentType != "application/json" {
		t.Fatalf("content type = %q, want the header the response carried", resp.ContentType)
	}
}

func TestExchangeFailureRetainsEarlierHopProgress(t *testing.T) {
	denied := errors.New("fixture hop denied")
	for _, denyAt := range []int{1, 2} {
		var sent atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sent.Add(1)
			w.Header().Set("Location", "/done")
			w.WriteHeader(http.StatusSeeOther)
		}))
		hop := 0
		_, err := Do(t.Context(), Request{Method: http.MethodPost, URL: server.URL + "/submit", Body: []byte("mutation"), Redirects: RedirectSafe, AllowAddress: allowLoopback, BeforeHop: func(context.Context, *url.URL) error {
			hop++
			if hop == denyAt {
				return denied
			}
			return nil
		}})
		server.Close()
		var exchange *ExchangeError
		if !errors.Is(err, denied) || !errors.As(err, &exchange) {
			t.Fatalf("lost typed cause or progress: %v", err)
		}
		want := denyAt - 1
		if exchange.Method != http.MethodPost || exchange.RequestsStarted != want || exchange.ResponsesReceived != want || int(sent.Load()) != want {
			t.Fatalf("observed progress disagrees with server: %+v sent=%d", exchange, sent.Load())
		}
		if want > 0 && exchange.LastStatus != http.StatusSeeOther {
			t.Fatalf("lost earlier response status: %+v", exchange)
		}
	}
}

func TestExchangeFailureRetainsHeadersBeforeSinkFailure(t *testing.T) {
	sinkErr := errors.New("fixture storage failure")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	}))
	defer server.Close()
	_, err := Do(t.Context(), Request{Method: http.MethodPost, URL: server.URL, AllowAddress: allowLoopback, StreamBody: func(io.Reader) error { return sinkErr }})
	var exchange *ExchangeError
	if !errors.Is(err, sinkErr) || !errors.As(err, &exchange) || exchange.RequestsStarted != 1 || exchange.ResponsesReceived != 1 || exchange.LastStatus != http.StatusCreated {
		t.Fatalf("storage failure hid completed HTTP exchange: %v", err)
	}
}
