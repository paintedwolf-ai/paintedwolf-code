package contract

import (
	"net/http"

	"github.com/lycaon/lycaon/pkg/api"
)

const fixtureSourceViewID = "bdb147bf-b55b-4347-9422-ecc67e04a7a3"
const fixtureSourcePresentationID = "766ad911-5b6d-48bc-984d-a74472044253"
const fixtureSourceInterestID = "e1b40d12-1d03-4c4c-9a84-cdc80f76d6cd"

func stubSourceTreeView() api.SourceTreeView {
	return api.SourceTreeView{
		Kind: "tree", ID: fixtureSourceViewID, IntentRevision: "intent-1", ProjectionRevision: "projection-1",
		State: "ready", Extent: api.SourceViewExtent{Complete: true}, ExpiresAt: fixtureTimeValue(),
		WorkspaceID: fixtureWorkspaceID, Roots: []api.ProjectRoot{},
	}
}

func registerStubSourceViewRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	const collection = "/v1/projects/{id}/source/views"
	const resource = collection + "/{view_id}"
	const presentation = resource + "/presentations/{presentation_id}"
	mux.HandleFunc("POST "+collection, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/v1/projects/"+fixtureProjectID+"/source/views/"+fixtureSourceViewID)
		writeJSON(w, http.StatusCreated, stubSourceTreeView())
	})
	for _, route := range []string{"GET " + resource, "POST " + resource + "/apply"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, stubSourceTreeView())
		})
	}
	mux.HandleFunc("DELETE "+resource, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /v1/projects/{id}/source/comparison-digests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceComparisonDigests{Digests: []api.SourceComparisonDigest{{InRange: false}}})
	})
	mux.HandleFunc("POST "+resource+"/presentations", func(w http.ResponseWriter, r *http.Request) {
		view := stubSourceTreeView()
		writeJSON(w, http.StatusCreated, api.SourcePresentation{ID: fixtureSourcePresentationID, View: api.SourceView{Tree: &view}})
	})
	mux.HandleFunc("DELETE "+presentation, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, method := range []string{"PUT", "DELETE"} {
		mux.HandleFunc(method+" "+resource+"/interests/{interest_id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	}
	address := api.SourceTreeAddress{RootID: fixtureProjectID, Path: "."}
	mux.HandleFunc("GET "+presentation+"/rows", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceTreeFrame{
			Kind: "tree", ViewID: fixtureSourceViewID, IntentRevision: "intent-1", ProjectionRevision: "projection-1",
			Extent: api.SourceViewExtent{Complete: true}, Anchor: address,
			Rows: []api.SourceTreeRow{}, Ancestors: []api.SourceTreeAncestor{},
		})
	})
	mux.HandleFunc("GET "+presentation+"/locate", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceTreeLocation{
			Kind: "tree", ViewID: fixtureSourceViewID, ProjectionRevision: "projection-1", Address: address,
		})
	})
	mux.HandleFunc("GET "+presentation+"/search", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceTreeSearchPage{
			Kind: "tree", ViewID: fixtureSourceViewID, ProjectionRevision: "projection-1", Complete: true, Matches: []api.SourceTreeSearchMatch{},
		})
	})
}
