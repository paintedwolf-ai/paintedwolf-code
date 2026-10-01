package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestAPIScanFeedbackAsyncEvidenceAndSummary(t *testing.T) {
	h := wiring.BuildForTest(t)
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	srv := h.Server
	projectDir := t.TempDir()
	project := createProjectHTTP(t, srv, projectDir)
	hostDir := h.HostProjectDir(t, project.ID)
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	created := enqueueSingleScanner(t, srv, project.ID, []wire.ScanCategory{wire.ScanCategorySCA})

	var got wire.CodeScan
	testutil.WaitFor(t, 15*time.Second, func() bool {
		getReq := authedRequest(t, http.MethodGet, scanURL(project.ID, created.ID)+"?view=summary", nil)
		getW := httptest.NewRecorder()
		srv.ServeHTTP(getW, getReq)
		if getW.Code != http.StatusOK {
			t.Fatalf("GET status = %d", getW.Code)
		}
		if err := json.Unmarshal(getW.Body.Bytes(), &got); err != nil {
			testutil.FailErr(t, "unmarshal JSON document", err)
		}
		return got.Status == wire.CodeScanStatusComplete
	})

	taskID := wire.ScanTaskID(created.ID)
	records, err := evidenceStore.ReadAll(context.Background(), hostDir, "", taskID, evidence.GateTypeSecurity)
	if err != nil || len(records) == 0 {
		t.Fatalf("evidence rows = %d err=%v hostDir=%s", len(records), err, hostDir)
	}
	ok, reason := inspector.EvidenceAnchored(records[len(records)-1])
	if !ok {
		t.Fatalf("expected anchored security evidence: %s", reason)
	}
}
