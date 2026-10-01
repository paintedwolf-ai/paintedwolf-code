package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceEditorConfigResolvesTheRootChain(t *testing.T) {
	srv := newTestServer(t)
	root := t.TempDir()
	p := createProjectForTest(t, srv, root)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(root, "pkg"), 0o755))
	testutil.FailErr(t, "write root config", os.WriteFile(filepath.Join(root, ".editorconfig"),
		[]byte("root = true\n[*]\ntrim_trailing_whitespace = true\ninsert_final_newline = false\n"), 0o600))
	testutil.FailErr(t, "write nested config", os.WriteFile(filepath.Join(root, "pkg", ".editorconfig"),
		[]byte("[*.py]\nindent_style = space\nindent_size = 4\nend_of_line = crlf\n"), 0o600))

	get := func(query url.Values) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/editorconfig?"+query.Encode(), nil))
		return rec
	}

	rec := get(url.Values{"root_id": {p.Roots[0].ID}, "path": {"pkg/new.py"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got wire.SourceEditorConfig
	testutil.FailErr(t, "decode response", json.Unmarshal(rec.Body.Bytes(), &got))
	if got.Path != "pkg/new.py" || got.IndentStyle != "space" || got.IndentSize != 4 || got.EndOfLine != "crlf" ||
		got.TrimTrailingWhitespace == nil || !*got.TrimTrailingWhitespace ||
		got.InsertFinalNewline == nil || *got.InsertFinalNewline {
		t.Fatalf("resolved = %+v", got)
	}

	if rec := get(url.Values{"root_id": {p.Roots[0].ID}, "path": {"../outside.py"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("traversal status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := get(url.Values{"path": {"pkg/new.py"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing root_id status = %d body=%s", rec.Code, rec.Body.String())
	}
}
