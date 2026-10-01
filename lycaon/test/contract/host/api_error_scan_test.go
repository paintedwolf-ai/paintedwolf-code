package contract

import (
	"os"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestAPIErrorScannerIncludesNestedHandlers(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"pkg/api/api_error_code_ids.generated.go": "package api\nconst ApiErrorCodeRejected = \"rejected\"\n",
		"internal/api/httpio/response.go": `package httpio
func respond(s *Responder) {
 s.Error(nil, 400, "invalid_json", "diagnostic")
 s.ErrorContext(nil, 400, wire.ApiErrorCodeRejected, nil, "diagnostic")
 s.ReasonError(nil, 400, "invalid_request", "diagnostic")
 s.Fail(nil, "body_too_large", "diagnostic")
 s.FailDetails(nil, "invalid_query", nil, "diagnostic")
}
`,
		"internal/api/httpio/response_test.go": "package httpio\nfunc fixture(s *Responder) { s.Error(nil, 400, \"test_only\", \"fixture\") }\n",
	}
	for path, body := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		contractcheck.FailErr(t, "create API scanner fixture", os.MkdirAll(filepath.Dir(full), 0o755))
		contractcheck.FailErr(t, "write API scanner fixture", os.WriteFile(full, []byte(body), 0o644))
	}
	codes := scanAPIErrorCodes(t, filepath.Join(root, "internal", "api"))
	for _, code := range []string{"invalid_json", "rejected", "invalid_request", "body_too_large", "invalid_query"} {
		if !codes[code] {
			t.Errorf("nested responder emission missing: %s", code)
		}
	}
	if codes["test_only"] {
		t.Fatal("scanner included a test fixture emission")
	}
}
