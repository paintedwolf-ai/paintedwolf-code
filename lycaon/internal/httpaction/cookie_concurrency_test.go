package httpaction

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestParallelHTTPRequestWorkersShareOneCookieJarWithoutSerializingNetwork(t *testing.T) {
	secrets, _ := testSecrets(t)
	const count = 4
	arrived, release := make(chan struct{}, count), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		http.SetCookie(w, &http.Cookie{Name: r.URL.Query().Get("worker"), Value: "private-session-value", Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	defer close(release)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", Register(reg, Deps{Boundary: testBoundary(), Secrets: secrets}))
	type response struct {
		out string
		err error
	}
	results := make(chan response, count)
	capability := loopbackCapability(t, server.URL)
	for i := range count {
		go func() {
			name := fmt.Sprintf("worker%d", i)
			out, err := reg.Run(t.Context(), "http_request", map[string]any{
				"url": server.URL + "?worker=" + name, "cookie_jar": "shared", "capability_request": capability,
			}, tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID,
					ProjectID:       testdbseed.DefaultProjectID,
					SessionID:       name,
					ParentSessionID: "root-1",
					ToolCallID:      "request"},
			})
			results <- response{out, err}
		}()
	}
	for range count {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("requests did not overlap; a cookie lock spans network traffic")
		}
	}
	// Broadcast without closing so cleanup can release blocked handlers on failure.
	for range count {
		release <- struct{}{}
	}
	var reference string
	for range count {
		res := <-results
		testutil.FailErr(t, "parallel HTTP request", res.err)
		var decoded result
		testutil.FailErr(t, "decode HTTP response", json.Unmarshal([]byte(res.out), &decoded))
		if decoded.Cookies == nil || !decoded.Cookies.Persisted || decoded.Cookies.Stored != 1 {
			t.Fatalf("cookie receipt = %+v", decoded.Cookies)
		}
		if reference != "" && reference != decoded.Cookies.Reference {
			t.Fatal("workers minted different capabilities")
		}
		reference = decoded.Cookies.Reference
	}
	jar, err := secrets.OpenCookieJar(t.Context(), secretcap.CookieJarRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1", OperationID: "read", Name: "shared",
	})
	testutil.FailErr(t, "reopen merged jar", err)
	if len(jar.Store.Snapshot()) != count {
		t.Fatal("worker cookie was lost")
	}
}

func TestCookieJarSavesCookiesReceivedBeforeBodyCancellation(t *testing.T) {
	secrets, _ := testSecrets(t)
	received := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "private-session-value", Path: "/"})
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(received)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", Register(reg, Deps{Boundary: testBoundary(), Secrets: secrets}))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err := reg.Run(ctx, "http_request", map[string]any{
		"url": server.URL, "cookie_jar": "session", "capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(t.TempDir(), "canceled-body"))
	if err == nil {
		t.Fatal("unfinished body unexpectedly succeeded")
	}
	select {
	case <-received:
	default:
		t.Fatal("request never received response headers")
	}
	jar, err := secrets.OpenCookieJar(t.Context(), secretcap.CookieJarRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1", OperationID: "read", Name: "session",
	})
	testutil.FailErr(t, "open jar after canceled body", err)
	if got := jar.Store.Snapshot(); len(got) != 1 || got[0].Name != "sid" {
		t.Fatal("cancellation discarded cookies received before the body failed")
	}
}
