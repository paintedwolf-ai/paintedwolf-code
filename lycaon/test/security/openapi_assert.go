package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	openapi "github.com/lycaon/lycaon/test/openapi"
)

// AssertResponseMatchesOpenAPI validates recorder output against docs/openapi.yaml.
func AssertResponseMatchesOpenAPI(t *testing.T, w *httptest.ResponseRecorder, method, pathTemplate string, pathParams map[string]string) {
	t.Helper()
	if err := openapi.ValidateResponse(context.Background(), method, pathTemplate, pathParams, w.Code, w.Header(), w.Body.Bytes()); err != nil {
		t.Fatalf("OpenAPI response mismatch for %s %s (status=%d): %v", method, pathTemplate, w.Code, err)
	}
}

// AssertHTTPResponseMatchesOpenAPI validates a real HTTP response against docs/openapi.yaml.
// Body bytes are passed explicitly because the caller has typically already drained Body.
func AssertHTTPResponseMatchesOpenAPI(t *testing.T, status int, hdr http.Header, body []byte, method, pathTemplate string, pathParams map[string]string) {
	t.Helper()
	if err := openapi.ValidateResponse(context.Background(), method, pathTemplate, pathParams, status, hdr, body); err != nil {
		t.Fatalf("OpenAPI response mismatch for %s %s (status=%d): %v", method, pathTemplate, status, err)
	}
}
