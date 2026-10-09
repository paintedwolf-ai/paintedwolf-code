package contractfixture

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func ComparisonRowsForTest(t *testing.T, server *hostapi.Server, projectID string, view wire.SourceComparisonView, offset int) wire.SourceComparisonFrame {
	t.Helper()
	presentation := PresentationForTest(t, server, projectID, view.ID, view.IntentRevision)
	query := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {"200"}}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, NewAuthedRequest(http.MethodGet, sourceapi.SourceViewURL(projectID, view.ID)+"/presentations/"+presentation.ID+"/rows?"+query.Encode(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("comparison rows: status=%d body=%s", response.Code, response.Body.String())
	}
	var frame wire.SourceComparisonFrame
	testutil.FailErr(t, "decode comparison rows", json.Unmarshal(response.Body.Bytes(), &frame))
	return frame
}

func ComparisonViewForTest(t *testing.T, server *hostapi.Server, projectID string, source wire.SourceComparisonSelector, mode string) wire.SourceComparisonView {
	t.Helper()
	encoded, err := json.Marshal(wire.SourceComparisonViewCreate{Kind: "comparison", OperationID: uuid.NewString(), ClientID: "test-window", Source: source, Intent: wire.SourceComparisonIntent{Mode: mode}})
	testutil.FailErr(t, "encode comparison view", err)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, NewAuthedRequest(http.MethodPost, "/v1/projects/"+projectID+"/source/views", bytes.NewReader(encoded)))
	state := ReadSourceViewResponse(t, response, http.StatusCreated)
	id := state.Comparison.ID
	t.Cleanup(func() {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, NewAuthedRequest(http.MethodDelete, sourceapi.SourceViewURL(projectID, id), nil))
	})
	testutil.WaitFor(t, 10*time.Second, func() bool {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, NewAuthedRequest(http.MethodGet, sourceapi.SourceViewURL(projectID, id), nil))
		state = ReadSourceViewResponse(t, response, http.StatusOK)
		return state.Comparison.State != "preparing"
	})
	return *state.Comparison
}

func DeletedSourceRowsForTest(t *testing.T, server *hostapi.Server, projectID string, deleted *wire.SourceDeletedFile) []wire.SourceReaderRow {
	t.Helper()
	if deleted == nil || deleted.Source == nil {
		return nil
	}
	view := ComparisonViewForTest(t, server, projectID, wire.SourceComparisonSelector{Version: deleted.Source}, "before")
	if view.State != "ready" {
		t.Fatalf("deleted comparison failed: %+v", view.Failure)
	}
	var rows []wire.SourceReaderRow
	for offset := 0; int64(offset) < view.Extent.Rows; {
		frame := ComparisonRowsForTest(t, server, projectID, view, offset)
		rows = append(rows, frame.Rows...)
		if frame.Span.End <= int64(offset) {
			t.Fatal("comparison page did not advance")
		}
		offset = int(frame.Span.End)
	}
	return rows
}

func DeletedSourceTextForTest(t *testing.T, server *hostapi.Server, projectID string, deleted *wire.SourceDeletedFile) string {
	t.Helper()
	var Text strings.Builder
	for _, row := range DeletedSourceRowsForTest(t, server, projectID, deleted) {
		Text.WriteString(row.Text)
	}
	return Text.String()
}

func PresentationForTest(t *testing.T, server *hostapi.Server, project, view, intent string) wire.SourcePresentation {
	t.Helper()
	body, err := json.Marshal(wire.SourcePresentationCreate{OperationID: uuid.NewString(), IntentRevision: intent})
	testutil.FailErr(t, "encode presentation", err)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, NewAuthedRequest(http.MethodPost, sourceapi.SourceViewURL(project, view)+"/presentations", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create presentation: %d %s", response.Code, response.Body.String())
	}
	var presentation wire.SourcePresentation
	testutil.FailErr(t, "decode presentation", json.Unmarshal(response.Body.Bytes(), &presentation))
	t.Cleanup(func() {
		server.ServeHTTP(httptest.NewRecorder(), NewAuthedRequest(http.MethodDelete, sourceapi.SourceViewURL(project, view)+"/presentations/"+presentation.ID, nil))
	})
	return presentation
}
