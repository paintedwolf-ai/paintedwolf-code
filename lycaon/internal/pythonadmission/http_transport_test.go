package pythonadmission

import (
	"context"
	"encoding/base64"
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
		Policy: func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})
	tr := NewHTTPTransport(HTTPTransportConfig{
		ExecutionID:        "exec-http-1",
		CallID:             "call-http-1",
		Observer:           obs,
		CredentialScreener: StrictCredentialScreener(),
		AllowLoopbackHTTP:  true,
		Evidence:           store,
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
		ExecutionID:        "exec-http-refuse",
		Observer:           obs,
		CredentialScreener: StrictCredentialScreener(),
		AllowLoopbackHTTP:  true,
		Evidence:           store,
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
		Policy: func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})
	tr := NewHTTPTransport(HTTPTransportConfig{
		ExecutionID:        "exec-stream",
		Observer:           obs,
		CredentialScreener: StrictCredentialScreener(),
		AllowLoopbackHTTP:  true,
		Evidence:           store,
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

	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-tls",
		Policy: func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})
	tr := NewHTTPTransport(HTTPTransportConfig{
		ExecutionID: "exec-tls",
		Observer:    obs,
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

func TestHTTPTransportFailsClosedWithoutObserverOrScreener(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Missing Observer must fail closed without connecting.
	trNoObs := NewHTTPTransport(HTTPTransportConfig{
		ExecutionID:        "exec-no-obs",
		CredentialScreener: StrictCredentialScreener(),
		AllowLoopbackHTTP:  true,
	})
	if _, err := trNoObs.Do(context.Background(), HTTPRequest{Method: http.MethodGet, URL: srv.URL}); !errors.Is(err, ErrMissingObserver) {
		t.Fatalf("nil observer err = %v, want ErrMissingObserver", err)
	}

	// Missing CredentialScreener must fail closed without connecting.
	trNoScreener := NewHTTPTransport(HTTPTransportConfig{
		ExecutionID:       "exec-no-screener",
		Observer:          NewAdmissionObserver(ObserverConfig{ExecutionID: "exec-no-screener"}),
		AllowLoopbackHTTP: true,
	})
	if _, err := trNoScreener.Do(context.Background(), HTTPRequest{Method: http.MethodGet, URL: srv.URL}); !errors.Is(err, ErrMissingCredentialScreener) {
		t.Fatalf("nil screener err = %v, want ErrMissingCredentialScreener", err)
	}

	if hits.Load() != 0 {
		t.Fatalf("server received %d hits on fail-closed transport, want 0", hits.Load())
	}
}

func TestGuestHTTPAdapterFullChainLoopback(t *testing.T) {
	var forbiddenHits atomic.Int32
	forbiddenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forbiddenHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer forbiddenSrv.Close()
	forbiddenURL, err := url.Parse(forbiddenSrv.URL)
	testutil.FailErr(t, "parse forbidden url", err)

	allowedDest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("redirect-ok"))
	}))
	defer allowedDest.Close()

	streamBlock := make(chan struct{})
	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("hello-guest"))
		case "/redir-ok":
			http.Redirect(w, r, allowedDest.URL+"/final", http.StatusFound)
		case "/redir-bad":
			http.Redirect(w, r, forbiddenSrv.URL+"/blocked", http.StatusFound)
		case "/stream":
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("first-chunk"))
			if flusher != nil {
				flusher.Flush()
			}
			<-streamBlock
			_, _ = w.Write([]byte("second-chunk"))
		}
	}))
	defer func() {
		close(streamBlock)
		mainSrv.Close()
	}()

	store := NewMemoryEvidenceStore()
	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-guest-http",
		CallID:      "call-guest-http",
		Evidence:    store,
		Policy: func(_ context.Context, op pythonruntime.ReachedOperation) PolicyDecision {
			if op.Family == pythonruntime.EffectHTTP && op.Target.RootID == forbiddenURL.Host {
				return PolicyDecision{
					Kind:      pythonruntime.DecisionStop,
					Condition: ConditionEffectDenied,
					Reason:    "forbidden destination",
				}
			}
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})
	tr := NewHTTPTransport(HTTPTransportConfig{
		ExecutionID:        "exec-guest-http",
		CallID:             "call-guest-http",
		Observer:           obs,
		CredentialScreener: StrictCredentialScreener(),
		AllowLoopbackHTTP:  true,
		Evidence:           store,
	})
	adapter := NewGuestHTTPAdapter(tr)

	// 1. Accepted destination through JSON bridge.
	rawReq, err := json.Marshal(GuestHTTPRequest{
		OperationID: "guest-ok",
		Method:      http.MethodGet,
		URL:         mainSrv.URL + "/ok",
	})
	testutil.FailErr(t, "marshal ok request", err)
	rawResp, err := adapter.HandleJSON(context.Background(), rawReq)
	testutil.FailErr(t, "handle ok request", err)
	var gresp GuestHTTPResponse
	testutil.FailErr(t, "decode ok response", json.Unmarshal(rawResp, &gresp))
	decoded, err := base64.StdEncoding.DecodeString(gresp.BodyBase64)
	testutil.FailErr(t, "decode ok body", err)
	if gresp.StatusCode != http.StatusOK || string(decoded) != "hello-guest" {
		t.Fatalf("unexpected ok guest response: status=%d body=%q", gresp.StatusCode, string(decoded))
	}

	// 2. Allowed redirect through JSON bridge.
	rawRedirOK, _ := json.Marshal(GuestHTTPRequest{
		OperationID: "guest-redir-ok",
		Method:      http.MethodGet,
		URL:         mainSrv.URL + "/redir-ok",
	})
	rawRedirOKResp, err := adapter.HandleJSON(context.Background(), rawRedirOK)
	testutil.FailErr(t, "handle redir-ok", err)
	var redirOKResp GuestHTTPResponse
	testutil.FailErr(t, "decode redir-ok", json.Unmarshal(rawRedirOKResp, &redirOKResp))
	if len(redirOKResp.Redirects) != 1 || !redirOKResp.Redirects[0].Admitted {
		t.Fatalf("expected 1 admitted redirect, got %+v", redirOKResp.Redirects)
	}

	// 3. Refused destination directly through JSON bridge.
	rawRefused, _ := json.Marshal(GuestHTTPRequest{
		OperationID: "guest-refused",
		Method:      http.MethodGet,
		URL:         forbiddenSrv.URL + "/secret",
	})
	rawRefusedResp, refusedErr := adapter.HandleJSON(context.Background(), rawRefused)
	if refusedErr == nil {
		t.Fatal("expected error for refused destination")
	}
	var refusedResp GuestHTTPResponse
	testutil.FailErr(t, "decode refused response", json.Unmarshal(rawRefusedResp, &refusedResp))
	if refusedResp.StopCode != pythonruntime.StopEffectNotAllowed {
		t.Fatalf("refused stop code = %q, want COMMAND_EFFECT_NOT_ALLOWED", refusedResp.StopCode)
	}

	// 4. Refused redirect through JSON bridge stops before hitting forbiddenSrv.
	rawRedirBad, _ := json.Marshal(GuestHTTPRequest{
		OperationID: "guest-redir-bad",
		Method:      http.MethodGet,
		URL:         mainSrv.URL + "/redir-bad",
	})
	rawRedirBadResp, redirBadErr := adapter.HandleJSON(context.Background(), rawRedirBad)
	if redirBadErr == nil {
		t.Fatal("expected error for refused redirect")
	}
	var redirBadResp GuestHTTPResponse
	testutil.FailErr(t, "decode redir-bad response", json.Unmarshal(rawRedirBadResp, &redirBadResp))
	if !redirBadResp.PartialFailure || redirBadResp.StopCode != pythonruntime.StopEffectNotAllowed {
		t.Fatalf("unexpected redir-bad response: %+v", redirBadResp)
	}
	if forbiddenHits.Load() != 0 {
		t.Fatalf("forbidden server hit %d times, want 0", forbiddenHits.Load())
	}

	// 5. Mid-stream cancellation through GuestHTTPAdapter.ReadStreamChunk.
	rawStream, _ := json.Marshal(GuestHTTPRequest{
		OperationID: "guest-stream",
		Method:      http.MethodGet,
		URL:         mainSrv.URL + "/stream",
		Stream:      true,
	})
	rawStreamResp, err := adapter.HandleJSON(context.Background(), rawStream)
	testutil.FailErr(t, "open guest stream", err)
	var streamResp GuestHTTPResponse
	testutil.FailErr(t, "decode stream response", json.Unmarshal(rawStreamResp, &streamResp))
	if streamResp.StreamHandleID == "" {
		t.Fatal("expected non-empty stream_handle_id")
	}

	chunk1Raw, err := adapter.ReadStreamChunk(context.Background(), streamResp.StreamHandleID, 64)
	testutil.FailErr(t, "read chunk 1", err)
	var chunk1 GuestHTTPStreamChunk
	testutil.FailErr(t, "decode chunk 1", json.Unmarshal(chunk1Raw, &chunk1))
	c1Bytes, _ := base64.StdEncoding.DecodeString(chunk1.DataBase64)
	if string(c1Bytes) != "first-chunk" {
		t.Fatalf("chunk 1 = %q, want %q", string(c1Bytes), "first-chunk")
	}

	cancelCtx, cancelStream := context.WithCancel(context.Background())
	cancelStream()
	chunk2Raw, cancelErr := adapter.ReadStreamChunk(cancelCtx, streamResp.StreamHandleID, 64)
	if !errors.Is(cancelErr, context.Canceled) {
		t.Fatalf("cancelled chunk err = %v, want context.Canceled", cancelErr)
	}
	var chunk2 GuestHTTPStreamChunk
	testutil.FailErr(t, "decode cancelled chunk", json.Unmarshal(chunk2Raw, &chunk2))
	if !chunk2.PartialFailure {
		t.Fatalf("expected PartialFailure=true on mid-stream cancel, got %+v", chunk2)
	}
}
