package outboundhttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
)

func allowLoopback(addr netip.Addr, _ uint16) bool { return addr.IsLoopback() }

func TestNormalizeURLDropsUnsentFragment(t *testing.T) {
	target, err := NormalizeURL("https://example.test/api#client-only")
	if err != nil {
		t.Fatalf("NormalizeURL: %v", err)
	}
	if got := target.String(); got != "https://example.test/api" {
		t.Fatalf("normalized URL = %q, want fragment-free wire destination", got)
	}
}

func TestDoPreservesMethodHeadersBodyAndResponseFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		if got := r.Header.Values("X-Ordered"); len(got) != 2 || got[0] != "first" || got[1] != "second" {
			t.Errorf("ordered header = %v", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if string(body) != `{"ok":true}` {
			t.Errorf("body = %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=secret")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	t.Cleanup(server.Close)

	response, err := Do(t.Context(), Request{
		Method: http.MethodPatch, URL: server.URL,
		OriginHeaders: []Header{{Name: "X-Ordered", Value: "first"}, {Name: "X-Ordered", Value: "second"}},
		Body:          []byte(`{"ok":true}`), AllowAddress: allowLoopback,
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	wantHash := sha256.Sum256([]byte(`{"created":true}`))
	if response.Status != http.StatusCreated || string(response.Body) != `{"created":true}` ||
		response.Bytes != int64(len(response.Body)) || response.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("response = %+v body=%q", response, response.Body)
	}
	for _, header := range response.Headers {
		if header.Name == "Set-Cookie" {
			t.Fatal("response exposed Set-Cookie")
		}
	}
}

func TestDoScopesHeadersAcrossRedirectOrigins(t *testing.T) {
	var authorization, cookie, apiKey, ordinary, hostHeader string
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization, cookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
		apiKey, ordinary = r.Header.Get("X-API-Key"), r.Header.Get("X-Keep")
		hostHeader = r.Header.Get("X-Host-Protocol")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(destination.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/ready", http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	response, err := Do(t.Context(), Request{
		URL: origin.URL, Redirects: RedirectSafe, AllowAddress: allowLoopback,
		HopHeaders: []Header{{Name: "X-Host-Protocol", Value: "fetch"}},
		OriginHeaders: []Header{
			{Name: "Authorization", Value: "Bearer secret"}, {Name: "Cookie", Value: "session=secret"},
			{Name: "X-API-Key", Value: "secret"}, {Name: "X-Keep", Value: "yes"},
		},
	})
	if err != nil {
		t.Fatalf("Do redirect: %v", err)
	}
	if response.Status != http.StatusNoContent || len(response.Redirects) != 1 {
		t.Fatalf("response = %+v", response)
	}
	if authorization != "" || cookie != "" || apiKey != "" || ordinary != "" {
		t.Fatalf("cross-origin redirect forwarded caller headers: authorization=%q cookie=%q api_key=%q ordinary=%q",
			authorization, cookie, apiKey, ordinary)
	}
	if hostHeader != "fetch" {
		t.Fatalf("cross-origin redirect dropped host protocol header: %q", hostHeader)
	}
}

func TestDoPreservesCallerHeadersAcrossSameOriginRedirect(t *testing.T) {
	var got string
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			http.Redirect(w, r, "/second", http.StatusFound)
			return
		}
		got = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusNoContent)
	})
	server.Start()
	t.Cleanup(server.Close)

	_, err := Do(t.Context(), Request{
		URL: server.URL + "/first", Redirects: RedirectSafe, AllowAddress: allowLoopback,
		OriginHeaders: []Header{{Name: "X-Request-ID", Value: "request-1"}},
	})
	if err != nil {
		t.Fatalf("Do same-origin redirect: %v", err)
	}
	if got != "request-1" {
		t.Fatalf("same-origin redirect header = %q, want request-1", got)
	}
}

func TestDoStartsIODeadlineAfterApprovalPause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	turnCtx, cancelTurn := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelTurn()
	stopCtx, cancelStop := context.WithCancel(context.Background())
	t.Cleanup(cancelStop)
	ctx := hitl.WithStopContext(turnCtx, stopCtx)

	response, err := Do(ctx, Request{
		URL: server.URL, Timeout: time.Second, AllowAddress: allowLoopback,
		BeforeHop: func(context.Context, *url.URL) error {
			<-turnCtx.Done()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("request after approval pause: %v", err)
	}
	if response.Status != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Status, http.StatusNoContent)
	}
}

func TestDoRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	t.Cleanup(server.Close)
	_, err := Do(t.Context(), Request{URL: server.URL, MaxBodyBytes: 4, AllowAddress: allowLoopback})
	var tooLarge *BodyTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Limit != 4 {
		t.Fatalf("error = %v, want four-byte BodyTooLargeError", err)
	}
}

func TestDoUsesOneNetworkTimeoutAcrossRedirects(t *testing.T) {
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(45 * time.Millisecond)
		if r.URL.Path == "/first" {
			http.Redirect(w, r, server.URL+"/second", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	server.Start()
	t.Cleanup(server.Close)
	_, err := Do(context.Background(), Request{
		URL: server.URL + "/first", Redirects: RedirectSafe,
		Timeout: 70 * time.Millisecond, AllowAddress: allowLoopback,
	})
	if err == nil {
		t.Fatal("redirect chain exceeded the total timeout")
	}
}

func TestDoRejectsHostControlledHeadersBeforeDial(t *testing.T) {
	cases := []Header{
		{Name: "Content-Length", Value: "7"},
		{Name: "Proxy-Authorization", Value: "Bearer forged"},
		{Name: "Bad Header", Value: "value"},
		{Name: " X-Test", Value: "value"},
		{Name: "X-Test", Value: "bad\x00value"},
	}
	for _, header := range cases {
		t.Run(header.Name, func(t *testing.T) {
			asked := false
			_, err := Do(t.Context(), Request{
				URL: "http://127.0.0.1:1", OriginHeaders: []Header{header},
				AllowAddress: allowLoopback,
				BeforeHop: func(context.Context, *url.URL) error {
					asked = true
					return nil
				},
			})
			if err == nil {
				t.Fatal("invalid header was accepted")
			}
			if asked {
				t.Fatal("invalid request reached the approval boundary")
			}
		})
	}
}

func TestDoPopulatesTiming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello timing"))
	}))
	t.Cleanup(server.Close)

	response, err := Do(t.Context(), Request{
		URL: server.URL, AllowAddress: allowLoopback,
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if response.Timing == nil {
		t.Fatal("response.Timing is nil")
	}
	if response.Timing.Total <= 0 {
		t.Errorf("expected positive total duration, got %d", response.Timing.Total)
	}
}

func TestDoExtractsTLSAndClassifiesFaults(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("secure content"))
	}))
	t.Cleanup(tlsServer.Close)

	// Without a trusted root, a self-signed server is an unknown-authority fault.
	_, err := Do(t.Context(), Request{
		URL: tlsServer.URL, AllowAddress: allowLoopback,
	})
	if err == nil {
		t.Fatal("expected TLS verification failure for self-signed cert")
	}
	var fault *TLSFault
	if !errors.As(err, &fault) {
		t.Fatalf("expected *TLSFault, got %T: %v", err, err)
	}
	if fault.Type != "unknown_authority" {
		t.Errorf("fault.Type = %q, want unknown_authority", fault.Type)
	}
	if !Permanent(err) {
		t.Error("expected TLS fault to be Permanent (retryable: false)")
	}
}

func TestDoHostHeaderAndResolveOverride(t *testing.T) {
	var observedHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedHost = r.Host
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	serverPort := uint16(80)
	if p := u.Port(); p != "" {
		parsed, _ := strconv.Atoi(p)
		serverPort = uint16(parsed)
	}

	// HostHeader sets the Host sent on the wire.
	_, err = Do(t.Context(), Request{
		URL: server.URL, HostHeader: "vhost.example.test", AllowAddress: allowLoopback,
	})
	if err != nil {
		t.Fatalf("Do with HostHeader: %v", err)
	}
	if observedHost != "vhost.example.test" {
		t.Errorf("observedHost = %q, want vhost.example.test", observedHost)
	}

	// A Host entry in OriginHeaders is rejected as host-controlled; HostHeader is the only door.
	_, err = Do(t.Context(), Request{
		URL: server.URL, OriginHeaders: []Header{{Name: "Host", Value: "header.vhost.test"}}, AllowAddress: allowLoopback,
	})
	if err == nil || !Permanent(err) {
		t.Fatalf("expected permanent fault for Host in OriginHeaders, got %v", err)
	}

	// Resolve dials the mapped address and keeps the URL's host.
	observedHost = ""
	_, err = Do(t.Context(), Request{
		URL: "http://resolved.example.test:" + strconv.Itoa(int(serverPort)),
		Resolve: []ResolveMapping{
			{Host: "resolved.example.test", Port: serverPort, Address: netip.MustParseAddr("127.0.0.1"), TargetPort: serverPort},
		},
		AllowAddress: allowLoopback,
	})
	if err != nil {
		t.Fatalf("Do with Resolve mapping: %v", err)
	}
	if observedHost != "resolved.example.test:"+strconv.Itoa(int(serverPort)) {
		t.Errorf("observedHost = %q, want resolved.example.test:%d", observedHost, serverPort)
	}
}
