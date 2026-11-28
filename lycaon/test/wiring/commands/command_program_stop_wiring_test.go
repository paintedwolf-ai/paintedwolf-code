package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// TestCommandProgramDevAppHTTPStopControls exercises the shipped Den session
// and project background-process stop HTTP endpoints over the full wired host,
// verifying immediate acknowledgment, eventual settlement, sibling isolation,
// and handle authorization.
func TestCommandProgramDevAppHTTPStopControls(t *testing.T) {
	p := newProgramHost(t)
	s1 := p.program(t, `sleep 300 & echo s1-ready`, "")
	s2 := p.program(t, `sleep 300 & echo s2-ready`, "")
	testutil.FailErr(t, "prompt s1", s1.prompt(t.Context()))
	testutil.FailErr(t, "prompt s2", s2.prompt(t.Context()))

	httpJSON := func(method, path string, dst any) (int, string) {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
		req.Header.Set("Authorization", "Bearer "+p.h.APIToken)
		w := httptest.NewRecorder()
		p.h.Server.ServeHTTP(w, req)
		body := w.Body.String()
		if dst != nil && w.Code == http.StatusOK {
			testutil.FailErr(t, method+" "+path+" decode", json.Unmarshal(w.Body.Bytes(), dst))
		}
		return w.Code, body
	}

	var list1, list2 wire.BackgroundProcessListResponse
	code, body := httpJSON(http.MethodGet, "/v1/sessions/"+s1.session+"/background", &list1)
	if code != http.StatusOK || len(list1.Processes) != 1 || !list1.Processes[0].Running {
		t.Fatalf("s1 background list status=%d body=%s", code, body)
	}
	h1 := list1.Processes[0].ProcessID

	code, body = httpJSON(http.MethodGet, "/v1/sessions/"+s2.session+"/background", &list2)
	if code != http.StatusOK || len(list2.Processes) != 1 || !list2.Processes[0].Running {
		t.Fatalf("s2 background list status=%d body=%s", code, body)
	}
	h2 := list2.Processes[0].ProcessID

	// Cross-session stop must be refused (404).
	if crossCode, crossBody := httpJSON(http.MethodPost, "/v1/sessions/"+s1.session+"/background/"+h2+"/stop", nil); crossCode != http.StatusNotFound {
		t.Fatalf("cross-session stop status=%d body=%s, want 404", crossCode, crossBody)
	}

	wantUser := wire.BackgroundProcessTerminationReason(bgprocess.TerminationStoppedByUser)
	// 1. Session control: POST /v1/sessions/{id}/background/{process_id}/stop
	var ack1 wire.BackgroundProcessStopResult
	code, body = httpJSON(http.MethodPost, "/v1/sessions/"+s1.session+"/background/"+h1+"/stop", &ack1)
	if code != http.StatusOK || !ack1.StopRequested || ack1.TerminationReason != wantUser {
		t.Fatalf("session stop ack status=%d body=%s", code, body)
	}
	t.Logf("dev-app session stop control: route=POST /v1/sessions/%s/background/%s/stop ack=%s", s1.session, h1, body)

	testutil.WaitFor(t, "s1 handle settles stopped_by_user", func() bool {
		var cur wire.BackgroundProcessListResponse
		c, _ := httpJSON(http.MethodGet, "/v1/sessions/"+s1.session+"/background", &cur)
		return c == http.StatusOK && len(cur.Processes) == 1 && !cur.Processes[0].Running && cur.Processes[0].TerminationReason == wantUser
	})

	// Sibling s2 handle must remain running.
	var sibling wire.BackgroundProcessListResponse
	code, body = httpJSON(http.MethodGet, "/v1/sessions/"+s2.session+"/background", &sibling)
	if code != http.StatusOK || len(sibling.Processes) != 1 || !sibling.Processes[0].Running {
		t.Fatalf("sibling s2 altered after s1 stop: status=%d body=%s", code, body)
	}
	t.Logf("sibling status after s1 stop: handle=%s running=%t", h2, sibling.Processes[0].Running)

	// 2. Project control: POST /v1/projects/{id}/background/{process_id}/stop
	var ack2 wire.BackgroundProcessStopResult
	code, body = httpJSON(http.MethodPost, "/v1/projects/"+s2.project+"/background/"+h2+"/stop", &ack2)
	if code != http.StatusOK || !ack2.StopRequested || ack2.TerminationReason != wantUser {
		t.Fatalf("project stop ack status=%d body=%s", code, body)
	}
	t.Logf("dev-app project stop control: route=POST /v1/projects/%s/background/%s/stop ack=%s", s2.project, h2, body)

	testutil.WaitFor(t, "s2 handle settles stopped_by_user in project view", func() bool {
		var cur wire.ProjectBackgroundProcessListResponse
		c, _ := httpJSON(http.MethodGet, "/v1/projects/"+s2.project+"/background", &cur)
		return c == http.StatusOK && len(cur.Processes) == 1 && !cur.Processes[0].Running && cur.Processes[0].TerminationReason == wantUser
	})
}
