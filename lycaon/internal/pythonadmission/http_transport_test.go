package pythonadmission

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHTTPTransportMethodsParamsJSONAndAllowedRedirect(t *testing.T) {
	var gotMethod, gotQuery, gotHeader string
	var gotBody map[string]any

	finalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"redirected":true}`))
	}))
	defer finalSrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/echo":
			gotMethod = r.Method
			gotQuery = r.URL.Query().Get("k")
			gotHeader = r.Header.Get("X-Test")
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/redir":
			http.Redirect(w, r, finalSrv.URL+"/dest", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiSrv.Close()

	store := NewMemoryEvidenceStore()
	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-http-1",
		CallID:      "call-http-1",
		Evidence:    store,
	})
	tr := NewHTTPTransport(HTTPTransportConfig{
		ExecutionID:       "exec-http-1",
		CallID:            "call-http-1",
		Observer:          obs,
		AllowLoopbackHTTP: true,
		Evidence:          store,
	})

	// 1. POST with query params, JSON body, and custom header.
	resp, err := tr.Do(context.Background(), HTTPRequest{
		OperationID: "http-echo",
		Method:      http.MethodPost,
		URL:         apiSrv.URL + "/echo",
		Params:      map[string]string{"k": "v1"},
		Headers:     map[string]string{"X-Test": "hello"},
		JSONBody:    map[string]any{"msg": "ping"},
		Timeout:     5 * time.Second,
	})
	testutil.FailErr(t, "http echo request", err)
	body, err := io.ReadAll(resp.Body)
	testutil.FailErr(t, "read echo body", err)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("echo response = (%d, %q), want (200, ok)", resp.StatusCode, string(body))
	}
	if gotMethod != http.MethodPost || gotQuery != "v1" || gotHeader != "hello" || gotBody["msg"] != "ping" {
		t.Fatalf("server observed method=%q query=%q header=%q body=%v", gotMethod, gotQuery, gotHeader, gotBody)
	}

	// 2. Allowed redirect re-enters OperationObserver.Admit(EffectHTTP) for destination URL.
	redirResp, err := tr.Do(context.Background(), HTTPRequest{
		OperationID: "http-redir-ok",
		Method:      http.MethodGet,
		URL:         apiSrv.URL + "/redir",
	})
	testutil.FailErr(t, "http redirect request", err)
	redirBody, err := io.ReadAll(redirResp.Body)
	testutil.FailErr(t, "read redirect body", err)
	_ = redirResp.Body.Close()

	if string(redirBody) != `{"redirected":true}` {
		t.Fatalf("redirect body = %q", string(redirBody))
	}
	if len(redirResp.Redirects) != 1 || !redirResp.Redirects[0].Admitted {
		t.Fatalf("expected 1 admitted redirect hop, got %+v", redirResp.Redirects)
	}

	// Confirm OperationObserver saw both initial request and redirect destination.
	sawRedirectAdmit := false
	for _, rec := range store.Admissions() {
		if rec.Operation == "http.redirect" && rec.Family == pythonruntime.EffectHTTP {
			sawRedirectAdmit = true
		}
	}
	if !sawRedirectAdmit {
		t.Fatal("OperationObserver did not record http.redirect admission")
	}
}

func TestHTTPTransportRefusedRedirectStopsBeforeConnecting(t *testing.T) {
	var forbiddenHits atomic.Int32
	forbiddenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forbiddenHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer forbiddenSrv.Close()
	forbiddenURL, err := url.Parse(forbiddenSrv.URL)
	testutil.FailErr(t, "parse forbidden url", err)

	entrySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, forbiddenSrv.URL+"/secret", http.StatusFound)
	}))
	defer entrySrv.Close()

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	latch := pythonruntime.NewStopLatch(cancel)
	store := NewMemoryEvidenceStore()

	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-http-refuse",
		StopLatch:   latch,
		Evidence:    store,
		Policy: func(_ context.Context, op pythonruntime.ReachedOperation) PolicyDecision {
			if op.Family == pythonruntime.EffectHTTP && op.Target.RootID == forbiddenURL.Host {
				return PolicyDecision{
					Kind:      pythonruntime.DecisionStop,
					Condition: ConditionEffectDenied,
					Reason:    "redirect target host forbidden",
				}
			}
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})

	tr := NewHTTPTransport(HTTPTransportConfig{
		ExecutionID:       "exec-http-refuse",
		Observer:          obs,
		AllowLoopbackHTTP: true,
		Evidence:          store,
	})

	_, reqErr := tr.Do(context.Background(), HTTPRequest{
		OperationID: "http-refuse-redir",
		Method:      http.MethodGet,
		URL:         entrySrv.URL + "/start",
	})
	var admErr *HTTPAdmissionError
	if !errors.As(reqErr, &admErr) || !admErr.Redirect {
		t.Fatalf("expected HTTPAdmissionError for redirect, got %v", reqErr)
	}
	if forbiddenHits.Load() != 0 {
		t.Fatalf("forbidden redirect host received %d hits, want 0", forbiddenHits.Load())
	}
	if !latch.Stopped() {
		t.Fatal("refused redirect did not latch StopLatch")
	}
	if admErr.Record == nil || !admErr.Record.PartialFailure || len(admErr.Record.Redirects) != 1 || admErr.Record.Redirects[0].Admitted {
		t.Fatalf("unexpected refused redirect HTTPRecord: %+v", admErr.Record)
	}
}

func TestHTTPTransportStreamingAndPartialFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream-ok":
			w.Header().Set("Content-Type", "text/plain")
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("chunk-1\n"))
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("chunk-2\n"))
		case "/stream-partial-fail":
			// Advertise 100 bytes, send 12 bytes, then abruptly close TCP connection.
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "no hijack", http.StatusInternalServerError)
				return
			}
			conn, buf, err := hj.Hijack()
			if err != nil {
				return
			}
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nhello-part!!")
			_ = buf.Flush()
			_ = conn.Close()
		}
	}))
	defer srv.Close()

	store := NewMemoryEvidenceStore()
	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-stream",
		Evidence:    store,
	})
	tr := NewHTTPTransport(HTTPTransportConfig{
		ExecutionID:       "exec-stream",
		Observer:          obs,
		AllowLoopbackHTTP: true,
		Evidence:          store,
	})

	// 1. Clean streaming read.
	resp, err := tr.Do(context.Background(), HTTPRequest{
		OperationID: "http-stream-ok",
		Method:      http.MethodGet,
		URL:         srv.URL + "/stream-ok",
		Stream:      true,
	})
	testutil.FailErr(t, "stream ok request", err)
	data, err := io.ReadAll(resp.Body)
	testutil.FailErr(t, "read stream ok", err)
	testutil.FailErr(t, "close stream ok", resp.Body.Close())
	if string(data) != "chunk-1\nchunk-2\n" || resp.Record.BytesReceived != int64(len(data)) || resp.Record.PartialFailure {
		t.Fatalf("unexpected stream ok result: data=%q rec=%+v", string(data), resp.Record)
	}

	// 2. Mid-stream partial failure must report exact delivered bytes and PartialFailure=true.
	respFail, err := tr.Do(context.Background(), HTTPRequest{
		OperationID: "http-stream-partial",
		Method:      http.MethodGet,
		URL:         srv.URL + "/stream-partial-fail",
		Stream:      true,
	})
	testutil.FailErr(t, "open partial fail stream", err)
	partialBytes, readErr := io.ReadAll(respFail.Body)
	_ = respFail.Body.Close()
	if readErr == nil {
		t.Fatal("expected unexpected EOF / read error on truncated stream")
	}
	if string(partialBytes) != "hello-part!!" {
		t.Fatalf("partial bytes = %q, want %q", string(partialBytes), "hello-part!!")
	}
	if !respFail.Record.PartialFailure || respFail.Record.BytesReceived != int64(len("hello-part!!")) {
		t.Fatalf("partial failure record inaccurate: %+v", respFail.Record)
	}
}

func TestHTTPTransportTLSEnforcementAndCredentialScreening(t *testing.T) {
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tls-ok:" + r.Header.Get("Authorization")))
	}))
	defer tlsSrv.Close()

	tr := NewHTTPTransport(HTTPTransportConfig{
		ExecutionID: "exec-tls",
		Client:      tlsSrv.Client(),
		CredentialScreener: func(u *url.URL, h http.Header) error {
			if h.Get("Authorization") != "" && strings.Contains(u.Path, "untrusted") {
				return errors.New("bearer token not allowed on untrusted path")
			}
			return nil
		},
	})

	// 1. Plain HTTP to non-loopback host is rejected under TLS enforcement.
	_, err := tr.Do(context.Background(), HTTPRequest{
		Method: http.MethodGet,
		URL:    "http://example.com/api",
	})
	if !errors.Is(err, ErrTLSRequired) {
		t.Fatalf("plain http err = %v, want ErrTLSRequired", err)
	}

	// 2. Credential screening rejects unauthorized recipient before connecting.
	_, err = tr.Do(context.Background(), HTTPRequest{
		Method:  http.MethodGet,
		URL:     tlsSrv.URL + "/untrusted",
		Headers: map[string]string{"Authorization": "Bearer secret"},
	})
	if !errors.Is(err, ErrCredentialRecipientDenied) {
		t.Fatalf("credential screening err = %v, want ErrCredentialRecipientDenied", err)
	}

	// 3. Allowed HTTPS request succeeds.
	resp, err := tr.Do(context.Background(), HTTPRequest{
		Method:  http.MethodGet,
		URL:     tlsSrv.URL + "/trusted",
		Headers: map[string]string{"Authorization": "Bearer ok"},
	})
	testutil.FailErr(t, "tls trusted request", err)
	body, err := io.ReadAll(resp.Body)
	testutil.FailErr(t, "read tls body", err)
	_ = resp.Body.Close()
	if string(body) != "tls-ok:Bearer ok" {
		t.Fatalf("tls body = %q, want %q", string(body), "tls-ok:Bearer ok")
	}
}
