package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestAPIScanEnqueuePollLedgerAndSSE(t *testing.T) {
	h := wiring.BuildForTest(t)
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	srv := h.Server
	hub := h.MemoryHub()
	projectDir := t.TempDir()
	project := createProjectHTTP(t, srv, projectDir)
	ch, unsubscribe, err := hub.Subscribe(context.Background(), events.Subscription{Project: project.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsubscribe()

	created := enqueueSingleScanner(t, srv, project.ID, []wire.ScanCategory{wire.ScanCategorySecret})

	var sawSSE bool
	testutil.WaitFor(t, 15*time.Second, func() bool {
		for {
			select {
			case envelope := <-ch:
				if envelope.Topic != wire.EventTopicScan {
					continue
				}
				var ev wire.CodeScanEvent
				if err := json.Unmarshal(envelope.Data, &ev); err != nil {
					testutil.FailErr(t, "unmarshal JSON document", err)
				}
				if ev.ScanID == created.ID && ev.Status == wire.CodeScanStatusComplete {
					sawSSE = true
				}
			default:
				goto polled
			}
		}
	polled:
		getReq := authedRequest(t, http.MethodGet, scanURL(project.ID, created.ID), nil)
		getW := httptest.NewRecorder()
		srv.ServeHTTP(getW, getReq)
		if getW.Code != http.StatusOK {
			t.Fatalf("GET status = %d", getW.Code)
		}
		var got wire.CodeScan
		if err := json.Unmarshal(getW.Body.Bytes(), &got); err != nil {
			testutil.FailErr(t, "unmarshal JSON document", err)
		}
		return got.Status == wire.CodeScanStatusComplete && sawSSE
	})

	if !sawSSE {
		t.Fatal("expected SSE complete event")
	}
}
