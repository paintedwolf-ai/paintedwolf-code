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

func TestReplaceApplyReopenPreservesDirtyDraftVersionAndRestore(t *testing.T) {
	beforeBytes := []byte("top\nmiddle alpha\nbottom\n")
	beforeHash := sha256.Sum256(beforeBytes)
	beforeHex := hex.EncodeToString(beforeHash[:])

	afterBytes := []byte("top\nmiddle beta\nbottom\n")
	afterHash := sha256.Sum256(afterBytes)
	afterHex := hex.EncodeToString(afterHash[:])

	f := contractfixture.NewSourceContentFixture(t, beforeBytes)
	root := f.Project.Roots[0]
	testutil.FailErr(t, "settle source inventory",
		catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), f.Project.ID, sourcecatalog.Root{ID: root.ID, Path: root.Path}))

	// 1. Open and inject a dirty unsaved draft.
	opened := f.Open(t)
	putW := contractfixture.SourceJSONRequest(t, f.Server, http.MethodPut, f.Url+"/editor-documents/"+opened.ID, wire.ReplaceEditorDocumentRequest{
		OperationID:      uuid.NewString(),
		ClientID:         "window",
		ExpectedRevision: opened.Revision,
		Content:          "top [draft]\nmiddle alpha\nbottom\n",
		EOL:              "lf",
		MixedEOL:         false,
	})
	drafted := contractfixture.DecodeSourceDocument(t, putW)
	if !drafted.Dirty {
		t.Fatalf("expected dirty draft, got %+v", drafted)
	}

	// 2. Preview and apply replacement across files via public HTTP routes.
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
	if len(preview.Files) != 1 {
		t.Fatalf("preview files = %+v", preview.Files)
	}

	applyW := contractfixture.SourceJSONRequest(t, f.Server, http.MethodPost, "/v1/search/replace/apply", wire.SearchReplaceApplyRequest{
		OperationID:     uuid.NewString(),
		Query:           "alpha",
		Replacement:     "beta",
		OriginProjectID: f.Project.ID,
		CaseSensitive:   true,
		Files: []wire.SearchReplaceApplyFile{
			{RootID: preview.Files[0].RootID, Path: preview.Files[0].Path, SHA256: preview.Files[0].SHA256, Hunks: []int{0}},
		},
	})
	if applyW.Code != http.StatusOK {
		t.Fatalf("apply status = %d body = %s", applyW.Code, applyW.Body.String())
	}

	// 3. Verify intended persistent bytes on disk.
	diskAfter, err := os.ReadFile(filepath.Join(root.Path, "content.txt"))
	testutil.FailErr(t, "read disk after replace", err)
	if string(diskAfter) != string(afterBytes) {
		t.Fatalf("disk after replace = %q, want %q", diskAfter, afterBytes)
	}

	// 4. Reopen under the public route and verify dirty draft preserved + journal consistent.
	reopened := f.Open(t)
	if reopened.ID != drafted.ID || reopened.FileID != drafted.FileID {
		t.Fatalf("reopened identity mismatch: got %+v want id=%s file_id=%s", reopened, drafted.ID, drafted.FileID)
	}
	if !reopened.Dirty || reopened.BaseSHA256 != afterHex {
		t.Fatalf("reopened state = dirty:%v base_sha256:%s, want dirty:true base_sha256:%s", reopened.Dirty, reopened.BaseSHA256, afterHex)
	}
	if gotDraft := f.Text(t, reopened); gotDraft != "top [draft]\nmiddle beta\nbottom\n" {
		t.Fatalf("reopened draft = %q, want %q", gotDraft, "top [draft]\nmiddle beta\nbottom\n")
	}

	// 5. Verify person-attributed version (origin == "user") and exact restoration to pre-replace bytes.
	vqs := url.Values{"file_id": {reopened.FileID}, "lane": {"retained"}, "limit": {"20"}}
	versW := httptest.NewRecorder()
	f.Server.ServeHTTP(versW, contractfixture.NewAuthedRequest(http.MethodGet, f.Url+"/source/versions?"+vqs.Encode(), nil))
	if versW.Code != http.StatusOK {
		t.Fatalf("versions status = %d body = %s", versW.Code, versW.Body.String())
	}
	var versResp struct {
		Versions []struct {
			ID            string `json:"id"`
			ContentSHA256 string `json:"content_sha256"`
			Origin        string `json:"origin"`
		} `json:"versions"`
	}
	testutil.FailErr(t, "decode versions", json.Unmarshal(versW.Body.Bytes(), &versResp))
	var beforeVerID string
	var sawUserAfter bool
	for _, v := range versResp.Versions {
		if v.ContentSHA256 == afterHex && v.Origin == "user" {
			sawUserAfter = true
		}
		if v.ContentSHA256 == beforeHex {
			beforeVerID = v.ID
		}
	}
	if !sawUserAfter || beforeVerID == "" {
		t.Fatalf("expected user-attributed after version and before version, got %+v", versResp.Versions)
	}

	restoreW := contractfixture.SourceJSONRequest(t, f.Server, http.MethodPost, f.Url+"/source/versions/"+beforeVerID+"/restore", map[string]any{
		"operation_id": uuid.NewString(),
		"file_id":      reopened.FileID,
		"root_id":      root.ID,
		"path":         "content.txt",
		"base":         map[string]any{"state": "content", "sha256": afterHex},
	})
	if restoreW.Code != http.StatusOK {
		t.Fatalf("restore status = %d body = %s", restoreW.Code, restoreW.Body.String())
	}
	restoredDisk, err := os.ReadFile(filepath.Join(root.Path, "content.txt"))
	testutil.FailErr(t, "read disk after restore", err)
	if string(restoredDisk) != string(beforeBytes) {
		t.Fatalf("restored disk = %q, want %q", restoredDisk, beforeBytes)
	}
}
