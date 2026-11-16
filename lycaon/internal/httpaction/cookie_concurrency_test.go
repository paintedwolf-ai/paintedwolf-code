package httpaction

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestParallelHTTPRequestWorkersShareOneCookieJarWithoutSerializingNetwork(t *testing.T) {
	approveDials(t)
	secrets, _ := testSecrets(t)
	const count = 4
	// A worker reports twice: when its request reaches the server and when its call returns.
	type report struct {
		worker   string
		returned bool
		out      string
		err      error
	}
	reports, release := make(chan report, 2*count), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reports <- report{worker: r.URL.Query().Get("worker")}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		http.SetCookie(w, &http.Cookie{Name: r.URL.Query().Get("worker"), Value: "private-session-value", Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	releaseAll := sync.OnceFunc(func() { close(release) })
	defer releaseAll()
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", Register(reg, Deps{Boundary: testBoundary(), Secrets: secrets}))
	capability := loopbackCapability(t, server.URL)
	var outstanding []string
	for i := range count {
		name := fmt.Sprintf("worker%d", i)
		outstanding = append(outstanding, name)
		go func() {
			out, err := reg.Run(t.Context(), "http_request", map[string]any{
				"url": server.URL + "?worker=" + name, "cookie_jar": "shared", "capability_request": capability,
			}, tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID,
					ProjectID:       testdbseed.DefaultProjectID,
					SessionID:       name,
					ParentSessionID: "root-1",
					ToolCallID:      "request"},
			})
			reports <- report{worker: name, returned: true, out: out, err: err}
		}()
	}
	// Every request must be at the server at once: a cookie lock spanning network traffic
	// would hold all but one back, and this wait would then name them.
	waiting := slices.Clone(outstanding)
	for len(waiting) > 0 {
		got := testutil.Receive(t, fmt.Sprintf("requests of %v reach the server", waiting), reports)
		if got.returned {
			t.Fatalf("%s returned before %v reached the server: err=%v out=%s", got.worker, waiting, got.err, got.out)
		}
		waiting = slices.DeleteFunc(waiting, func(name string) bool { return name == got.worker })
	}
	releaseAll()
	var reference string
	for len(outstanding) > 0 {
		res := testutil.Receive(t, fmt.Sprintf("calls of %v return", outstanding), reports)
		outstanding = slices.DeleteFunc(outstanding, func(name string) bool { return name == res.worker })
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
	approveDials(t)
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
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The client exposes no event for headers arriving, so allow their delivery after the server flushes.
	go func() {
		<-received
		time.AfterFunc(testutil.Timeout(2*time.Second), cancel)
	}()
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
