package toolusage

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLiveRequestOwnsDestinationAuthenticationAndReplayBody(t *testing.T) {
	client := newLiveClient("http://127.0.0.1:8855", "fixture-token")
	for _, path := range []string{"https://other.invalid/v1/sessions", "//other.invalid/path", "relative", "/path#fragment", "%invalid"} {
		if _, err := client.newRequest(t.Context(), http.MethodPost, path, nil); err == nil {
			t.Fatalf("accepted non-sidecar path %q", path)
		}
	}
	body := `{"text":"https://other.invalid/ is task data"}`
	req, err := client.newRequest(t.Context(), http.MethodPost, "/v1/sessions/session/prompts?after=5", strings.NewReader(body))
	testutil.FailErr(t, "construct sidecar request", err)
	if req.URL.Host != "127.0.0.1:8855" || req.URL.RawQuery != "after=5" || req.Header.Get("Authorization") != "Bearer fixture-token" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request lost its destination or authentication: %s", req.URL)
	}
	replay, err := req.GetBody()
	testutil.FailErr(t, "rebuild request body", err)
	defer func() { _ = replay.Close() }()
	got, err := io.ReadAll(replay)
	testutil.FailErr(t, "read replay body", err)
	if string(got) != body {
		t.Fatal("replay changed the prompt")
	}
}

func TestLiveClientDoesNotFollowSidecarRedirect(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer sidecar.Close()
	client := newLiveClient(sidecar.URL, "fixture-token")
	req, err := client.newRequest(t.Context(), http.MethodGet, "/v1/sessions", nil)
	testutil.FailErr(t, "build sidecar request", err)
	resp, err := client.do(req)
	testutil.FailErr(t, "read sidecar redirect", err)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound || redirected.Load() != 0 {
		t.Fatal("sidecar request changed destinations")
	}
}
