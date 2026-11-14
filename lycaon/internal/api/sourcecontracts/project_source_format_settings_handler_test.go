package sourcecontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceFormatSettingsResolvesEachToolForThePath(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	root := t.TempDir()
	p := contractfixture.CreateProjectForTest(t, srv, root)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(root, "pkg"), 0o755))
	testutil.FailErr(t, "write root config", os.WriteFile(filepath.Join(root, ".editorconfig"),
		[]byte("root = true\n[*]\ntrim_trailing_whitespace = true\ninsert_final_newline = false\n"), 0o600))
	testutil.FailErr(t, "write nested config", os.WriteFile(filepath.Join(root, "pkg", ".editorconfig"),
		[]byte("[*.py]\nindent_style = space\nindent_size = 4\nend_of_line = crlf\nmax_line_length = 99\n"), 0o600))
	testutil.FailErr(t, "write ruff config", os.WriteFile(filepath.Join(root, "pyproject.toml"),
		[]byte("[tool.ruff]\nline-length = 100\n"), 0o600))

	get := func(query url.Values) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/format-settings?"+query.Encode(), nil))
		return rec
	}

	rec := get(url.Values{"root_id": {p.Roots[0].ID}, "path": {"pkg/new.py"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got wire.SourceFormatSettings
	testutil.FailErr(t, "decode response", json.Unmarshal(rec.Body.Bytes(), &got))
	if got.Path != "pkg/new.py" || len(got.Sources) != 2 {
		t.Fatalf("resolved = %+v", got)
	}
	ec, ruff := got.Sources[0], got.Sources[1]
	if ec.Tool != wire.FormatToolEditorConfig || len(ec.Configs) != 2 || ec.Configs[0] != "pkg/.editorconfig" ||
		ec.Settings.IndentStyle != "space" || ec.Settings.IndentSize != 4 || ec.Settings.EndOfLine != "crlf" ||
		ec.Settings.LineWidth != 99 ||
		ec.Settings.TrimTrailingWhitespace == nil || !*ec.Settings.TrimTrailingWhitespace ||
		ec.Settings.InsertFinalNewline == nil || *ec.Settings.InsertFinalNewline {
		t.Fatalf("editorconfig = %+v", ec)
	}
	if ruff.Tool != wire.FormatToolRuff || ruff.Configs[0] != "pyproject.toml" || ruff.Settings.LineWidth != 100 {
		t.Fatalf("ruff = %+v", ruff)
	}
	if !slices.Contains(got.ConfigNames, ".editorconfig") || !slices.Contains(got.ConfigNames, "pyproject.toml") {
		t.Fatalf("config names = %v", got.ConfigNames)
	}

	if rec := get(url.Values{"root_id": {p.Roots[0].ID}, "path": {"../outside.py"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("traversal status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := get(url.Values{"path": {"pkg/new.py"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing root_id status = %d body=%s", rec.Code, rec.Body.String())
	}
}
