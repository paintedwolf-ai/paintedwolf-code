package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectSourceReadAnswersNonEditableContentWithoutBytes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		content    []byte
		wantStatus int
	}{
		{name: "binary metadata", path: ".DS_Store", content: []byte("book\x00keeping"), wantStatus: http.StatusOK},
		{name: "unsupported encoding refusal", path: "latin1.txt", content: []byte("caf\xe9\n"), wantStatus: http.StatusUnsupportedMediaType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ledgerDB, withLedger := testSourceLedger(t)
			srv := newTestServer(t, withLedger)
			rootPath := t.TempDir()
			p := createProjectForTest(t, srv, rootPath)
			mirrorLedgerProject(t, ledgerDB, p)
			testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(rootPath, tc.path), tc.content, 0o644))

			req := newAuthedRequest(http.MethodGet,
				"/v1/projects/"+p.ID+"/source?root_id="+p.Roots[0].ID+"&path="+tc.path, nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}

			if tc.wantStatus == http.StatusOK {
				var response wire.ProjectSourceReadResponse
				testutil.FailErr(t, "decode response", json.Unmarshal(w.Body.Bytes(), &response))
				if !response.Binary || response.Content != "" || response.SHA256 != "" {
					t.Fatalf("editor projection leaked revision bytes: %+v", response)
				}
			}
		})
	}
}
