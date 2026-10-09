package editoradmin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestOpenEditorDocumentValidatesRequestFields(t *testing.T) {
	f := newEditorFixture(t)
	rootID := f.project.Roots[0].ID
	cases := []struct {
		name  string
		body  any
		code  wire.ApiErrorCode
		field string
	}{
		{name: "malformed", body: "{", code: wire.ApiErrorCodeInvalidJson},
		{name: "path", body: wire.OpenEditorDocumentRequest{RootID: rootID, ClientID: "window"}, code: wire.ApiErrorCodeInvalidRequest, field: "path"},
		{name: "root", body: wire.OpenEditorDocumentRequest{Path: "a.txt", RootID: "root", ClientID: "window"}, code: wire.ApiErrorCodeInvalidRequest, field: "root_id"},
		{name: "client", body: wire.OpenEditorDocumentRequest{Path: "a.txt", RootID: rootID, ClientID: " "}, code: wire.ApiErrorCodeInvalidRequest, field: "client_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failure := expectCode(t, "open "+tc.name, f.serve(t, http.MethodPost, f.projectPath(""), tc.body), tc.code)
			if tc.field != "" && failure.Details["field"] != tc.field {
				t.Fatalf("refused field = %v, want %s", failure.Details["field"], tc.field)
			}
		})
	}
}

func TestOpenEditorDocumentRefusesUnavailableTargets(t *testing.T) {
	f := newEditorFixture(t)
	testutil.FailErr(t, "write binary fixture", os.WriteFile(filepath.Join(f.root, "blob.bin"), []byte{0, 1, 2, 0, 0xff}, 0o644))
	open := func(target, path string) *httptest.ResponseRecorder {
		return f.serve(t, http.MethodPost, target, wire.OpenEditorDocumentRequest{Path: path, RootID: f.project.Roots[0].ID, ClientID: "window"})
	}
	expectCode(t, "unknown project", open("/v1/projects/"+uuid.NewString()+"/editor-documents", "a.txt"), wire.ApiErrorCodeProjectNotFound)
	expectCode(t, "unplaced chat", open(f.projectPath("?session_id="+uuid.NewString()), "a.txt"), wire.ApiErrorCodeSessionNotFound)
	expectCode(t, "binary file", open(f.projectPath(""), "blob.bin"), wire.ApiErrorCodeSourceBinary)

	testutil.FailErr(t, "begin project mutation", f.h.MutationGate.BeginMutation(f.project.ID))
	expectCode(t, "changing project", open(f.projectPath(""), "a.txt"), wire.ApiErrorCodeProjectMutationInProgress)
	f.h.MutationGate.EndMutation(f.project.ID)
	f.open(t, "a.txt", "window")
}

func TestReopenOmitsBaseTheRetainedReplicaAlreadyHolds(t *testing.T) {
	f := newEditorFixture(t)
	dirty := f.replace(t, f.open(t, "a.txt", "window"), "beta\n")
	if !dirty.Dirty || dirty.BaseContent == nil || *dirty.BaseContent != "alpha\n" {
		t.Fatalf("dirty draft = %+v, want the disk base carried", dirty)
	}
	request := wire.OpenEditorDocumentRequest{Path: "a.txt", RootID: f.project.Roots[0].ID, ClientID: "window"}
	fresh := decodeStatus[wire.EditorDocument](t, "reopen without replica", f.serve(t, http.MethodPost, f.projectPath(""), request), http.StatusCreated)
	if fresh.ID != dirty.ID || fresh.BaseContent == nil {
		t.Fatalf("reopen without replica = %+v, want the same document with its base", fresh)
	}
	request.Replica = &wire.RetainedReplica{DocumentID: fresh.ID, Epoch: fresh.Epoch, StateVector: fresh.StateVector, BaseSHA256: fresh.BaseSHA256}
	retained := decodeStatus[wire.EditorDocument](t, "reopen with replica", f.serve(t, http.MethodPost, f.projectPath(""), request), http.StatusCreated)
	if retained.BaseContent != nil {
		t.Fatal("reopen resent the base a retained replica already holds")
	}
}

func TestEditorDocumentAddressingRefusesForeignDocuments(t *testing.T) {
	f := newEditorFixture(t)
	missing := f.documentPath(uuid.NewString(), "")
	expectCode(t, "missing document", f.serve(t, http.MethodPut, missing, wire.ReplaceEditorDocumentRequest{}), wire.ApiErrorCodeEditorDocumentNotFound)
	d := f.open(t, "a.txt", "window")
	other := "/v1/projects/" + uuid.NewString() + "/editor-documents/" + d.ID + "/observe"
	expectCode(t, "foreign project", f.serve(t, http.MethodPost, other, wire.ObserveEditorDocumentRequest{ClientID: "window"}), wire.ApiErrorCodeProjectNotFound)
}

func TestDocumentCommandsRejectMalformedBodies(t *testing.T) {
	f := newEditorFixture(t)
	d := f.open(t, "a.txt", "window")
	for _, route := range []struct{ method, suffix string }{
		{http.MethodPut, ""}, {http.MethodPost, "/sync"}, {http.MethodPost, "/updates"}, {http.MethodPost, "/presence"},
		{http.MethodPost, "/leave"}, {http.MethodPost, "/discard"}, {http.MethodPost, "/reload"}, {http.MethodPost, "/observe"},
		{http.MethodPost, "/save"}, {http.MethodPost, "/snapshots"}, {http.MethodPost, "/resolve"},
		{http.MethodPost, "/changes/" + uuid.NewString() + "/revert"}, {http.MethodPost, "/secret-spans/preview"},
		{http.MethodPost, "/secret-spans/mark"},
	} {
		response := f.serve(t, route.method, f.documentPath(d.ID, route.suffix), "{")
		expectCode(t, route.method+" "+route.suffix, response, wire.ApiErrorCodeInvalidJson)
	}
	if f.promotions.count() != 0 {
		t.Fatal("a refused leave retried project promotion")
	}
}

func TestDocumentCommandsWaitForProjectMutation(t *testing.T) {
	f := newEditorFixture(t)
	d := f.open(t, "a.txt", "window")
	testutil.FailErr(t, "begin project mutation", f.h.MutationGate.BeginMutation(f.project.ID))
	defer f.h.MutationGate.EndMutation(f.project.ID)
	for _, route := range []struct{ method, suffix string }{
		{http.MethodPut, ""}, {http.MethodPost, "/sync"}, {http.MethodPost, "/updates"}, {http.MethodPost, "/presence"},
		{http.MethodPost, "/leave"}, {http.MethodPost, "/discard"}, {http.MethodPost, "/reload"}, {http.MethodPost, "/observe"},
		{http.MethodPost, "/save"}, {http.MethodPost, "/snapshots"}, {http.MethodPost, "/resolve"}, {http.MethodGet, "/changes"},
		{http.MethodPost, "/changes/" + uuid.NewString() + "/revert"},
	} {
		response := f.serve(t, route.method, f.documentPath(d.ID, route.suffix), "{}")
		expectCode(t, route.method+" "+route.suffix, response, wire.ApiErrorCodeProjectMutationInProgress)
	}
	for _, target := range []string{f.projectPath("/status"), f.projectPath("/retention")} {
		method := http.MethodPost
		body := `{"document_ids":["` + d.ID + `"]}`
		if target == f.projectPath("/retention") {
			method, body = http.MethodPut, `{"client_id":"window","retained_clients":["window"],"document_ids":[]}`
		}
		expectCode(t, target, f.serve(t, method, target, body), wire.ApiErrorCodeProjectMutationInProgress)
	}
}

func TestReplicaSynchronizationSubmissionAndPresence(t *testing.T) {
	f := newEditorFixture(t)
	d := f.open(t, "a.txt", "window")
	incarnation := uuid.NewString()
	sync := func(base string) wire.EditorReplicaFrame {
		t.Helper()
		response := f.serve(t, http.MethodPost, f.documentPath(d.ID, "/sync"), wire.SyncEditorDocumentRequest{
			ClientID: "window", Incarnation: incarnation, Epoch: d.Epoch, BaseSHA256: base,
		})
		return decodeStatus[wire.EditorReplicaFrame](t, "sync replica", response, http.StatusOK)
	}
	if frame := sync(d.BaseSHA256); frame.BaseContent != nil {
		t.Fatal("sync resent a base the replica holds")
	}
	frame := sync("")
	if frame.BaseContent == nil || *frame.BaseContent != "alpha\n" || frame.ReplicaID == 0 {
		t.Fatalf("sync frame = %+v, want the unknown base and a replica identity", frame)
	}
	refused := f.serve(t, http.MethodPost, f.documentPath(d.ID, "/sync"), wire.SyncEditorDocumentRequest{ClientID: "window", Epoch: d.Epoch})
	expectCode(t, "sync without incarnation", refused, wire.ApiErrorCodeEditorReplicaIdentity)

	engine, err := documentcore.New(t.Context())
	testutil.FailErr(t, "start peer engine", err)
	t.Cleanup(func() { testutil.FailErr(t, "close peer engine", engine.Close(context.Background())) })
	_, err = engine.Call(t.Context(), documentcore.Request{Action: "open", Handle: 1, Client: frame.ReplicaID, Update: frame.CRDTUpdate})
	testutil.FailErr(t, "hydrate peer", err)
	change, err := engine.Call(t.Context(), documentcore.Request{Action: "edit", Handle: 1, Edits: []documentcore.Edit{{Index: 5, Insert: "!"}}})
	testutil.FailErr(t, "type in peer", err)
	submission := wire.SubmitEditorDocumentUpdateRequest{
		ClientID: "window", ReplicaID: frame.ReplicaID, Epoch: frame.Epoch, OperationID: uuid.NewString(),
		Update: change.Update, StateVector: change.Vector, BaseSHA256: frame.BaseSHA256,
	}
	accepted := decodeStatus[wire.EditorReplicaFrame](t, "submit update", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/updates"), submission), http.StatusOK)
	if accepted.AcceptedOperationID != submission.OperationID || accepted.AcceptedRevision <= d.Revision || !accepted.Dirty {
		t.Fatalf("accepted frame = %+v", accepted)
	}
	submission.Update = append([]byte(nil), frame.CRDTUpdate...)
	expectCode(t, "reused operation", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/updates"), submission), wire.ApiErrorCodeIdempotencyConflict)
	submission.ReplicaID, submission.OperationID = 0, uuid.NewString()
	expectCode(t, "anonymous replica", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/updates"), submission), wire.ApiErrorCodeEditorReplicaIdentity)

	presence := wire.UpdateEditorDocumentPresenceRequest{ClientID: "window", Incarnation: incarnation,
		Ranges: []wire.EditorPresenceRange{{Anchor: []byte{1}, Head: []byte{2}}}}
	if response := f.serve(t, http.MethodPost, f.documentPath(d.ID, "/presence"), presence); response.Code != http.StatusNoContent {
		t.Fatalf("presence = %d: %s", response.Code, response.Body.String())
	}
	observed := decodeStatus[wire.EditorDocument](t, "observe", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/observe"), wire.ObserveEditorDocumentRequest{ClientID: "window"}), http.StatusOK)
	if len(observed.Participants) != 1 || len(observed.Participants[0].Ranges) != 1 || observed.Participants[0].PersonID != f.owner.ID {
		t.Fatalf("participants = %+v, want the owner's selection", observed.Participants)
	}
	presence.Incarnation = uuid.NewString()
	expectCode(t, "stale presence", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/presence"), presence), wire.ApiErrorCodeEditorReplicaIdentity)

	leave := wire.LeaveEditorDocumentRequest{ClientID: "window", Incarnation: incarnation}
	if response := f.serve(t, http.MethodPost, f.documentPath(d.ID, "/leave"), leave); response.Code != http.StatusNoContent {
		t.Fatalf("leave = %d: %s", response.Code, response.Body.String())
	}
	if f.promotions.count() != 1 || f.promotions.projects[0] != f.project.ID {
		t.Fatalf("promotion retries = %v, want one for the project", f.promotions.projects)
	}
	gone := f.serve(t, http.MethodPost, f.documentPath(uuid.NewString(), "/leave"), leave)
	expectCode(t, "leave missing document", gone, wire.ApiErrorCodeEditorDocumentNotFound)
	if f.promotions.count() != 1 {
		t.Fatal("a refused leave retried project promotion")
	}
}

func TestDraftCommandsSaveDiscardAndReload(t *testing.T) {
	f := newEditorFixture(t)
	d := f.open(t, "a.txt", "window")
	refused := f.serve(t, http.MethodPut, f.documentPath(d.ID, ""), wire.ReplaceEditorDocumentRequest{
		OperationID: uuid.NewString(), ClientID: "window", ExpectedRevision: d.Revision, Content: "x", EOL: "cr",
	})
	expectCode(t, "invalid line ending", refused, wire.ApiErrorCodeInvalidRequest)
	d = f.replace(t, d, "saved\n")

	save := wire.SaveEditorDocumentRequest{ClientID: "window", ExpectedRevision: d.Revision, OperationID: "not-a-uuid"}
	expectCode(t, "save operation", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/save"), save), wire.ApiErrorCodeInvalidRequest)
	save.OperationID, save.ExpectedRevision = uuid.NewString(), d.Revision+7
	expectCode(t, "stale save", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/save"), save), wire.ApiErrorCodeEditorRevisionConflict)
	save.OperationID, save.ExpectedRevision = uuid.NewString(), d.Revision
	saved := decodeStatus[wire.EditorDocument](t, "save", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/save"), save), http.StatusOK)
	if saved.Dirty || f.disk(t, "a.txt") != "saved\n" {
		t.Fatalf("saved = %+v disk = %q", saved, f.disk(t, "a.txt"))
	}

	discarded := f.discard(t, f.replace(t, saved, "scratch\n"))
	if discarded.Dirty || discarded.BaseContent != nil {
		t.Fatalf("discarded = %+v, want the clean disk text", discarded)
	}
	testutil.FailErr(t, "change file on disk", os.WriteFile(filepath.Join(f.root, "a.txt"), []byte("external\n"), 0o644))
	reload := wire.EditorDocumentCommandRequest{OperationID: uuid.NewString(), ClientID: "window", ExpectedRevision: discarded.Revision}
	reloaded := decodeStatus[wire.EditorDocument](t, "reload", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/reload"), reload), http.StatusOK)
	if reloaded.Revision <= discarded.Revision || reloaded.Dirty || reloaded.Diverged {
		t.Fatalf("reloaded = %+v", reloaded)
	}
	reload.OperationID = "retry"
	expectCode(t, "reload operation", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/reload"), reload), wire.ApiErrorCodeIdempotencyConflict)
	discard := wire.EditorDocumentCommandRequest{OperationID: uuid.NewString(), ClientID: "window", ExpectedRevision: 1}
	expectCode(t, "stale discard", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/discard"), discard), wire.ApiErrorCodeEditorRevisionConflict)
}

func TestSnapshotsAndConflictResolution(t *testing.T) {
	f := newEditorFixture(t)
	d := f.replace(t, f.open(t, "a.txt", "window"), "draft\n")
	pinned := decodeStatus[wire.EditorDocument](t, "pin", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/snapshots"), wire.PinEditorDocumentRequest{ClientID: "window"}), http.StatusCreated)
	if pinned.Revision != d.Revision {
		t.Fatalf("pinned revision = %d, want %d", pinned.Revision, d.Revision)
	}
	reserved := wire.PinEditorDocumentRequest{ClientID: "window", OperationID: uuid.NewString()}
	decodeStatus[wire.EditorDocument](t, "pin for save", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/snapshots"), reserved), http.StatusCreated)
	expectCode(t, "pin missing", f.serve(t, http.MethodPost, f.documentPath(uuid.NewString(), "/snapshots"), reserved), wire.ApiErrorCodeEditorDocumentNotFound)

	resolve := wire.ResolveEditorDocumentRequest{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision,
		DiskSHA256: d.BaseSHA256, Content: "merged\n", EOL: "crlf-ish"}
	expectCode(t, "resolve line ending", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/resolve"), resolve), wire.ApiErrorCodeInvalidRequest)
	resolve.EOL, resolve.DiskSHA256 = "lf", "stale"
	expectCode(t, "resolve stale disk", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/resolve"), resolve), wire.ApiErrorCodeEditorRevisionConflict)
	resolve.OperationID, resolve.DiskSHA256 = uuid.NewString(), d.BaseSHA256
	resolved := decodeStatus[wire.EditorDocument](t, "resolve", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/resolve"), resolve), http.StatusOK)
	if resolved.Dirty || f.disk(t, "a.txt") != "merged\n" {
		t.Fatalf("resolved = %+v disk = %q", resolved, f.disk(t, "a.txt"))
	}
}

func TestDocumentErrorsMapToStructuredCodes(t *testing.T) {
	f := newEditorFixture(t)
	cases := []struct {
		err  error
		code wire.ApiErrorCode
	}{
		{&documentcore.Rejected{Code: "stale_update"}, wire.ApiErrorCodeInvalidRequest},
		{projectsource.ErrSourceEncodingInvalid, wire.ApiErrorCodeInvalidRequest},
		{projectsource.ErrSourceBusy, wire.ApiErrorCodeSourcePathBusy},
		{editordoc.ErrInvalidEOL, wire.ApiErrorCodeInvalidRequest},
		{textfile.ErrRawTooLarge, wire.ApiErrorCodeSourceContentTooLarge},
		{projectsource.ErrSourceWriteTooLarge, wire.ApiErrorCodeSourceContentTooLarge},
		{textfile.ErrBinary, wire.ApiErrorCodeInvalidRequest},
		{projectsource.ErrSourceNotFound, wire.ApiErrorCodeEditorDocumentNotFound},
		{editordoc.ErrOperationConflict, wire.ApiErrorCodeIdempotencyConflict},
		{projectsource.ErrSourceWriteConflict, wire.ApiErrorCodeEditorRevisionConflict},
		{editordoc.ErrReplicaEpoch, wire.ApiErrorCodeEditorReplicaEpoch},
		{editordoc.ErrReplicaIdentity, wire.ApiErrorCodeEditorReplicaIdentity},
		{editordoc.ErrRootDetached, wire.ApiErrorCodeEditorRootDetached},
		{projectsource.ErrSourcePathDenied, wire.ApiErrorCodeSourcePathDenied},
		{editordoc.ErrReadOnly, wire.ApiErrorCodeSourceReadOnly},
		{projectsource.ErrSourceBinary, wire.ApiErrorCodeSourceBinary},
		{errors.New("store offline"), wire.ApiErrorCodeInternalError},
	}
	for _, tc := range cases {
		response := httptest.NewRecorder()
		f.h.writeEditorDocumentError(response, httptest.NewRequest(http.MethodPost, "/", nil), fmt.Errorf("wrapped: %w", tc.err))
		failure := expectCode(t, tc.err.Error(), response, tc.code)
		var rejected *documentcore.Rejected
		if errors.As(tc.err, &rejected) && failure.Details["reject_code"] != "stale_update" {
			t.Fatalf("rejection details = %v", failure.Details)
		}
	}
}
