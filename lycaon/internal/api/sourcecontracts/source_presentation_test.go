package sourcecontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourcePresentationsKeepCoordinatesAcrossDisclosureCommands(t *testing.T) {
	server := contractfixture.NewTestServer(t)
	root := t.TempDir()
	for i := range 20 {
		dir := filepath.Join(root, "dir-"+strconv.Itoa(i))
		testutil.FailErr(t, "create directory", os.Mkdir(dir, 0700))
		testutil.FailErr(t, "create file", os.WriteFile(filepath.Join(dir, "file.txt"), []byte("source"), 0600))
	}
	project := contractfixture.CreateProjectForTest(t, server, root)
	physical, err := server.Sources.Workspace.ProjectRegistry.Get(t.Context(), project.ID)
	testutil.FailErr(t, "resolve workspace", err)
	call := func(method, id string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		testutil.FailErr(t, "encode request", err)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, contractfixture.NewAuthedRequest(method, strings.TrimSuffix(sourceapi.SourceViewURL(project.ID, id), "/"), bytes.NewReader(encoded)))
		return response
	}
	apply := func(id string, body any) *httptest.ResponseRecorder {
		t.Helper()
		return call(http.MethodPost, id+"/apply", body)
	}
	request := wire.SourceTreeViewCreate{Kind: "tree", ClientID: "test", OperationID: uuid.NewString(), WorkspaceID: physical.WorkspaceID()}
	created := contractfixture.ReadSourceViewResponse(t, call(http.MethodPost, "", request), http.StatusCreated).Tree
	ready := func() *wire.SourceTreeView {
		t.Helper()
		var state *wire.SourceTreeView
		testutil.WaitFor(t, 10*time.Second, func() bool {
			state = contractfixture.ReadSourceViewResponse(t, call(http.MethodGet, created.ID, nil), http.StatusOK).Tree
			return state.State == "ready"
		})
		if !state.Extent.Complete {
			t.Fatal("ready view published an incomplete extent")
		}
		return state
	}
	first := ready()
	collapsed := contractfixture.PresentationForTest(t, server, project.ID, first.ID, first.IntentRevision)
	requestUpdate := wire.SourceTreeViewUpdate{Kind: "tree", OperationID: uuid.NewString(), ExpectedIntentRevision: first.IntentRevision,
		Command: wire.SourceTreeCommand{Disclose: &wire.SourceTreeDisclose{Kind: "disclose", Disclosures: []wire.SourceTreeDisclosure{{Address: wire.SourceTreeAddress{RootID: project.Roots[0].ID, Path: "."}, Open: true, Recursive: true}}}}}
	contractfixture.ReadSourceViewResponse(t, apply(first.ID, requestUpdate), http.StatusOK)
	second := ready()
	expanded := contractfixture.PresentationForTest(t, server, project.ID, second.ID, second.IntentRevision)
	interestPath := first.ID + "/interests/" + uuid.NewString()
	demand := wire.SourceViewportInterest{PresentationID: expanded.ID, Sequence: 2, Start: 20, End: 41, Direction: -1}
	if response := call(http.MethodPut, interestPath, demand); response.Code != http.StatusNoContent {
		t.Fatalf("replace viewport: %d %s", response.Code, response.Body.String())
	}
	demand.Sequence = 1
	if response := call(http.MethodPut, interestPath, demand); response.Code != http.StatusNoContent {
		t.Fatalf("reordered viewport: %d %s", response.Code, response.Body.String())
	}
	if after := ready(); after.IntentRevision != second.IntentRevision || after.Extent != second.Extent {
		t.Fatal("viewport demand changed the presentation")
	}
	if response := call(http.MethodDelete, interestPath, nil); response.Code != http.StatusNoContent {
		t.Fatalf("release viewport: %d %s", response.Code, response.Body.String())
	}
	for _, presentation := range []wire.SourcePresentation{expanded, collapsed, expanded, collapsed} {
		total := presentation.View.Tree.Extent.Rows
		for _, offset := range []int64{total - 1, 0, total / 2} {
			response := httptest.NewRecorder()
			url := sourceapi.SourceViewURL(project.ID, first.ID) + "/presentations/" + presentation.ID + "/rows?offset=" + strconv.FormatInt(offset, 10) + "&limit=7"
			server.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodGet, url, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("rows: %d %s", response.Code, response.Body.String())
			}
			var frame wire.SourceTreeFrame
			testutil.FailErr(t, "decode frame", json.Unmarshal(response.Body.Bytes(), &frame))
			if frame.Extent != presentation.View.Tree.Extent || frame.IntentRevision != presentation.View.Tree.IntentRevision {
				t.Fatalf("presentation moved: %+v", frame)
			}
			for _, row := range frame.Rows {
				if row.Kind == "loading" {
					t.Fatal("published presentation contains loading rows")
				}
			}
		}
	}
	if collapsed.View.Tree.Extent.Rows != 21 || expanded.View.Tree.Extent.Rows != 41 {
		t.Fatalf("extents: %v %v", collapsed.View.Tree.Extent, expanded.View.Tree.Extent)
	}
	current := second
	rejected := wire.SourceTreeViewUpdate{Kind: "tree", OperationID: uuid.NewString(), ExpectedIntentRevision: current.IntentRevision,
		BasePresentationID: collapsed.ID,
		Command:            wire.SourceTreeCommand{Toggle: &wire.SourceTreeToggle{Kind: "toggle", Address: wire.SourceTreeAddress{RootID: project.Roots[0].ID, Path: "/"}}}}
	if response := apply(first.ID, rejected); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid displayed-basis toggle: %d %s", response.Code, response.Body.String())
	}
	var retry wire.SourceTreeViewUpdate
	commands := []wire.SourceTreeCommand{
		{Disclose: &wire.SourceTreeDisclose{Kind: "disclose", Disclosures: []wire.SourceTreeDisclosure{{Address: wire.SourceTreeAddress{RootID: project.Roots[0].ID, Path: "dir-0"}, Open: true}}}},
		{Toggle: &wire.SourceTreeToggle{Kind: "toggle", Address: wire.SourceTreeAddress{RootID: project.Roots[0].ID, Path: "dir-1"}}},
	}
	for index, command := range commands {
		update := wire.SourceTreeViewUpdate{Kind: "tree", OperationID: uuid.NewString(), ExpectedIntentRevision: current.IntentRevision,
			BasePresentationID: collapsed.ID,
			Command:            command}
		current = contractfixture.ReadSourceViewResponse(t, apply(first.ID, update), http.StatusOK).Tree
		if index == 0 {
			retry = update
		}
	}
	current = ready()
	if current.Extent.Rows != 23 {
		t.Fatalf("folder navigation did not supersede pending recursive intent or compose toggles: %+v", current)
	}
	if response := call(http.MethodDelete, first.ID+"/presentations/"+collapsed.ID, nil); response.Code != http.StatusNoContent {
		t.Fatalf("release displayed basis: %d %s", response.Code, response.Body.String())
	}
	contractfixture.ReadSourceViewResponse(t, apply(first.ID, retry), http.StatusOK)
	if after := ready(); after.Extent.Rows != 23 || after.IntentRevision != current.IntentRevision {
		t.Fatalf("retry reapplied navigation after its basis expired: %+v", after)
	}
}

func TestManualSourceTreeDisclosureExcludesRecursiveBulkCommands(t *testing.T) {
	address := wire.SourceTreeAddress{RootID: "root", Path: "."}
	if !sourceapi.ManualSourceTreeDisclosure(&wire.SourceTreeDisclose{Disclosures: []wire.SourceTreeDisclosure{{Address: address, Open: true}}}) {
		t.Fatal("ordinary folder disclosure was not classified as manual navigation")
	}
	for _, disclosures := range [][]wire.SourceTreeDisclosure{
		{{Address: address, Open: true, Recursive: true}},
		{{Address: address, Open: false, Recursive: true}, {Address: address, Open: true}},
	} {
		if sourceapi.ManualSourceTreeDisclosure(&wire.SourceTreeDisclose{Disclosures: disclosures}) {
			t.Fatalf("recursive bulk disclosure was classified as manual navigation: %+v", disclosures)
		}
	}
}

func TestSourceTreeUpdateReportsAForeignDisplayedBasisAsAChangedPresentation(t *testing.T) {
	server := contractfixture.NewTestServer(t)
	root := t.TempDir()
	testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Join(root, "dir"), 0700))
	testutil.FailErr(t, "create file", os.WriteFile(filepath.Join(root, "dir", "file.txt"), []byte("source"), 0600))
	project := contractfixture.CreateProjectForTest(t, server, root)
	physical, err := server.Sources.Workspace.ProjectRegistry.Get(t.Context(), project.ID)
	testutil.FailErr(t, "resolve workspace", err)
	call := func(method, id string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		testutil.FailErr(t, "encode request", err)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, contractfixture.NewAuthedRequest(method, strings.TrimSuffix(sourceapi.SourceViewURL(project.ID, id), "/"), bytes.NewReader(encoded)))
		return response
	}
	apply := func(id string, body any) *httptest.ResponseRecorder {
		t.Helper()
		return call(http.MethodPost, id+"/apply", body)
	}
	ready := func() *wire.SourceTreeView {
		t.Helper()
		created := contractfixture.ReadSourceViewResponse(t, call(http.MethodPost, "", wire.SourceTreeViewCreate{Kind: "tree", ClientID: "test", OperationID: uuid.NewString(), WorkspaceID: physical.WorkspaceID()}), http.StatusCreated).Tree
		var state *wire.SourceTreeView
		testutil.WaitFor(t, 10*time.Second, func() bool {
			state = contractfixture.ReadSourceViewResponse(t, call(http.MethodGet, created.ID, nil), http.StatusOK).Tree
			return state.State == "ready"
		})
		return state
	}
	older, current := ready(), ready()
	foreign := contractfixture.PresentationForTest(t, server, project.ID, older.ID, older.IntentRevision)
	reveal := func(base string) *httptest.ResponseRecorder {
		return apply(current.ID, wire.SourceTreeViewUpdate{Kind: "tree", OperationID: uuid.NewString(), ExpectedIntentRevision: current.IntentRevision, BasePresentationID: base,
			Command: wire.SourceTreeCommand{Reveal: &wire.SourceTreeReveal{Kind: "reveal", Address: wire.SourceTreeAddress{RootID: project.Roots[0].ID, Path: "dir/file.txt"}}}})
	}
	// A stale basis is a changed presentation, not a missing view.
	for name, base := range map[string]string{"another view's": foreign.ID, "unknown": uuid.NewString()} {
		response := reveal(base)
		var failure wire.ErrorResponse
		testutil.FailErr(t, "decode failure", json.Unmarshal(response.Body.Bytes(), &failure))
		if response.Code != http.StatusConflict || failure.Code != "source_view_revision_changed" {
			t.Fatalf("%s basis: %d %s", name, response.Code, response.Body.String())
		}
	}
	contractfixture.ReadSourceViewResponse(t, reveal(""), http.StatusOK)
}
