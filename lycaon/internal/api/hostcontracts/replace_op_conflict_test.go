package hostcontracts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestApplySearchReplacementBusyPathReturnsSourcePathBusyAndRecovers(t *testing.T) {
	initial := []byte("top\nmiddle alpha\nbottom\n")
	f := contractfixture.NewSourceContentFixture(t, initial)
	root := f.Project.Roots[0]
	testutil.FailErr(t, "settle source inventory",
		catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), f.Project.ID, sourcecatalog.Root{ID: root.ID, Path: root.Path}))

	doc := f.Open(t)
	draftContent := "top [unsaved draft]\nmiddle alpha\nbottom\n"
	putW := contractfixture.SourceJSONRequest(t, f.Server, http.MethodPut, f.Url+"/editor-documents/"+doc.ID, wire.ReplaceEditorDocumentRequest{
		OperationID:      uuid.NewString(),
		ClientID:         "window",
		ExpectedRevision: doc.Revision,
		Content:          draftContent,
		EOL:              "lf",
		MixedEOL:         false,
	})
	updatedDoc := contractfixture.DecodeSourceDocument(t, putW)
	if !updatedDoc.Dirty {
		t.Fatalf("expected dirty draft before replace, got %+v", updatedDoc)
	}

	previewW := contractfixture.SourceJSONRequest(t, f.Server, http.MethodPost, "/v1/search/replace/preview", wire.SearchReplacePreviewRequest{
		Query:           "alpha",
		Replacement:     "beta",
		OriginProjectID: f.Project.ID,
		CaseSensitive:   true,
	})
	if previewW.Code != http.StatusOK {
		t.Fatalf("preview status = %d body = %s", previewW.Code, previewW.Body.String())
	}
	var preview wire.SearchReplacePreviewResponse
	testutil.FailErr(t, "decode preview", json.Unmarshal(previewW.Body.Bytes(), &preview))
	if len(preview.Files) != 1 || preview.Files[0].Path != "content.txt" {
		t.Fatalf("unexpected preview files: %+v", preview.Files)
	}

	// Deterministically hold an overlapping file operation reservation on content.txt.
	release, err := f.Server.Sources.Mutations.SourceMutations.Paths.ReserveSourcePath(root.Path, "content.txt")
	testutil.FailErr(t, "reserve overlapping source path", err)

	applyReq := wire.SearchReplaceApplyRequest{
		OperationID:     uuid.NewString(),
		Query:           "alpha",
		Replacement:     "beta",
		OriginProjectID: f.Project.ID,
		CaseSensitive:   true,
		Files: []wire.SearchReplaceApplyFile{
			{RootID: preview.Files[0].RootID, Path: preview.Files[0].Path, SHA256: preview.Files[0].SHA256, Hunks: []int{0}},
		},
	}
	busyW := contractfixture.SourceJSONRequest(t, f.Server, http.MethodPost, "/v1/search/replace/apply", applyReq)
	if busyW.Code != http.StatusConflict {
		t.Fatalf("busy replace/apply status = %d, want %d (409 Conflict); body = %s", busyW.Code, http.StatusConflict, busyW.Body.String())
	}
	var apiErr struct {
		Code wire.ApiErrorCode `json:"code"`
	}
	testutil.FailErr(t, "decode busy error", json.Unmarshal(busyW.Body.Bytes(), &apiErr))
	if apiErr.Code != wire.ApiErrorCodeSourcePathBusy {
		t.Fatalf("busy error code = %q, want %q", apiErr.Code, wire.ApiErrorCodeSourcePathBusy)
	}

	// Verify disk bytes, version history, and unsaved draft remained consistent.
	gotDisk, err := os.ReadFile(filepath.Join(root.Path, "content.txt"))
	testutil.FailErr(t, "read disk after busy", err)
	if string(gotDisk) != string(initial) {
		t.Fatalf("disk bytes changed during busy conflict: got %q want %q", gotDisk, initial)
	}
	if gotDraft := f.Text(t, updatedDoc); gotDraft != draftContent {
		t.Fatalf("draft changed during busy conflict: got %q want %q", gotDraft, draftContent)
	}
	vqs := url.Values{"file_id": {updatedDoc.FileID}, "lane": {"retained"}, "limit": {"20"}}
	versW := httptest.NewRecorder()
	f.Server.ServeHTTP(versW, contractfixture.NewAuthedRequest(http.MethodGet, f.Url+"/source/versions?"+vqs.Encode(), nil))
	if versW.Code != http.StatusOK {
		t.Fatalf("versions status = %d body = %s", versW.Code, versW.Body.String())
	}
	var versBefore struct {
		Versions []struct {
			ContentSHA256 string `json:"content_sha256"`
			Origin        string `json:"origin"`
		} `json:"versions"`
	}
	testutil.FailErr(t, "decode versions before recovery", json.Unmarshal(versW.Body.Bytes(), &versBefore))
	wantAfter := []byte("top\nmiddle beta\nbottom\n")
	wantAfterHash := sha256.Sum256(wantAfter)
	wantAfterHex := hex.EncodeToString(wantAfterHash[:])
	for _, v := range versBefore.Versions {
		if v.ContentSHA256 == wantAfterHex {
			t.Fatalf("unexpected post-replace version recorded during busy conflict: %+v", versBefore.Versions)
		}
	}

	// Release the conflicting reservation and verify a later explicit apply succeeds.
	release()

	okW := contractfixture.SourceJSONRequest(t, f.Server, http.MethodPost, "/v1/search/replace/apply", applyReq)
	if okW.Code != http.StatusOK {
		t.Fatalf("recovered replace/apply status = %d body = %s", okW.Code, okW.Body.String())
	}
	var applyResp wire.SearchReplaceApplyResponse
	testutil.FailErr(t, "decode recovered apply", json.Unmarshal(okW.Body.Bytes(), &applyResp))
	if applyResp.BatchID != applyReq.OperationID || len(applyResp.Files) != 1 || !applyResp.Files[0].Applied {
		t.Fatalf("unexpected recovered apply response: %+v", applyResp)
	}
	gotAfter, err := os.ReadFile(filepath.Join(root.Path, "content.txt"))
	testutil.FailErr(t, "read disk after recovery", err)
	if string(gotAfter) != string(wantAfter) {
		t.Fatalf("disk bytes after recovery = %q, want %q", gotAfter, wantAfter)
	}
}
