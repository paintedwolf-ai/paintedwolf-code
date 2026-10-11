package sourcecontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestEditorOpenEncodingRefusalsPreserveSource(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	for _, tc := range []struct {
		name     string
		content  []byte
		decodeAs string
		status   int
		code     string
	}{
		{"malformed-utf8", []byte("prefix\n\xff\xfe\x80\nsuffix\n"), "", 415, "unsupported_encoding"},
		{"truncated-utf8", []byte("prefix\xc3"), "", 415, "unsupported_encoding"},
		{"codepage", []byte("caf\xe9\n"), "", 415, "unsupported_encoding"},
		{"odd-utf16", []byte{0xff, 0xfe, 'x'}, "", 415, "unsupported_encoding"},
		{"invalid-override", []byte("valid text\n"), "utf-16le", 400, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(f.Project.Roots[0].Path, tc.name+".txt")
			testutil.FailErr(t, "write encoding fixture", os.WriteFile(path, tc.content, 0o600))
			endpoint := "/editor-documents"
			body, err := json.Marshal(wire.OpenEditorDocumentRequest{Path: filepath.Base(path), RootID: f.Project.Roots[0].ID, ClientID: "encoding-test", DecodeAs: tc.decodeAs})
			testutil.FailErr(t, "encode open request", err)
			response := httptest.NewRecorder()
			f.Srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+f.Project.ID+endpoint, bytes.NewReader(body)))
			var refused wire.ErrorResponse
			testutil.FailErr(t, "decode encoding refusal", json.Unmarshal(response.Body.Bytes(), &refused))
			if response.Code != tc.status || string(refused.Code) != tc.code {
				t.Fatalf("%s: status=%d code=%q, want %d %q", endpoint, response.Code, refused.Code, tc.status, tc.code)
			}
			if tc.code == "unsupported_encoding" && refused.Details["detected"] == nil {
				t.Fatalf("%s: encoding refusal lost detected encoding", endpoint)
			}
			got, err := os.ReadFile(path)
			testutil.FailErr(t, "read refused source", err)
			if !bytes.Equal(got, tc.content) {
				t.Fatalf("%s: refusal changed source bytes", endpoint)
			}
		})
	}
}
