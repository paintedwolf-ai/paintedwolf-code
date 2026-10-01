package observability

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHTTPDebugEnabled(t *testing.T) {
	t.Setenv("LYCAON_HTTP_DEBUG", "")
	CloseHTTPDebug()
	if HTTPDebugEnabled() {
		t.Fatal("expected disabled by default")
	}
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	if !HTTPDebugEnabled() {
		t.Fatal("expected enabled")
	}
}

func TestHTTPDebugMiddlewareSkipsHealth(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	CloseHTTPDebug()

	handler := middleware.RequestID(HTTPDebugMiddleware(HTTPBodyCapturePolicy{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if _, err := os.Stat(logPath); err == nil {
		data, _ := os.ReadFile(logPath)
		if len(data) > 0 {
			t.Fatalf("health should not be logged: %q", data)
		}
	}
}

func TestHTTPDebugMiddlewareCapturesExchange(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	CloseHTTPDebug()

	handler := middleware.RequestID(HTTPDebugMiddleware(HTTPBodyCapturePolicy{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sess-1"}`))
	})))
	body := strings.NewReader(`{"text":"hello"}`)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/abc/prompts", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read file", err)
	var entry httpDebugEntry
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Method != http.MethodPost || entry.Path != "/v1/sessions/abc/prompts" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Status != http.StatusCreated {
		t.Fatalf("status = %d", entry.Status)
	}
	if !strings.Contains(entry.RequestBody, "hello") {
		t.Fatalf("request body = %q", entry.RequestBody)
	}
	if !strings.Contains(entry.ResponseBody, "sess-1") {
		t.Fatalf("response body = %q", entry.ResponseBody)
	}
	if entry.DurationMs < 0 {
		t.Fatalf("duration_ms = %d", entry.DurationMs)
	}
	if entry.StartedAt.IsZero() {
		t.Fatal("expected started_at")
	}
}

func TestHTTPDebugMiddlewareRedactsJSONSecretsInBody(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	CloseHTTPDebug()

	handler := HTTPDebugMiddleware(HTTPBodyCapturePolicy{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/v1/providers/x/credential", strings.NewReader(`{"api_key":"secret-value"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read file", err)
	if strings.Contains(string(data), "secret-value") {
		t.Fatalf("leaked secret: %s", data)
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("expected redaction: %s", data)
	}
}

func TestHTTPDebugMiddlewareMarksStreamPaths(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	CloseHTTPDebug()

	handler := HTTPDebugMiddleware(HTTPBodyCapturePolicy{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/events?project_id=p1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read file", err)
	var entry httpDebugEntry
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if !entry.Stream {
		t.Fatal("expected stream=true")
	}
	if entry.ResponseBody != "" {
		t.Fatalf("stream path should not capture response body: %q", entry.ResponseBody)
	}
}

func TestHTTPDebugMiddlewareForwardsFullRequestBody(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	t.Setenv("LYCAON_HTTP_DEBUG_MAX_BYTES", "32")
	CloseHTTPDebug()

	const full = `{"text":"look","images":[{"mime":"image/png","bytes":"QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ejAxMjM0NTY3ODk="}]}`
	if len(full) <= 32 {
		t.Fatalf("fixture must exceed the debug cap; got %d", len(full))
	}

	var gotBody string
	handler := HTTPDebugMiddleware(HTTPBodyCapturePolicy{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		testutil.FailErr(t, "handler ReadAll", err)
		gotBody = string(raw)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/abc/prompts", strings.NewReader(full))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotBody != full {
		t.Fatalf("handler body truncated: got %d want %d bytes", len(gotBody), len(full))
	}
	if !json.Valid([]byte(gotBody)) {
		t.Fatalf("handler body is not valid JSON")
	}

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read file", err)
	var entry httpDebugEntry
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if !entry.Truncated {
		t.Fatal("expected truncated=true on the log sample")
	}
	if entry.RequestBody != oversizeRequestBody || entry.RequestBytes != int64(len(full)) {
		t.Fatalf("logged request = %q (%d bytes)", entry.RequestBody, entry.RequestBytes)
	}
}

func TestHTTPDebugMiddlewareDoesNotReadRequestBeforeHandler(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	CloseHTTPDebug()

	body := &countingReader{source: strings.NewReader(`{"text":"hello"}`)}
	readsBeforeHandler := -1
	handler := HTTPDebugMiddleware(HTTPBodyCapturePolicy{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readsBeforeHandler = body.reads
		_, err := io.ReadAll(r.Body)
		testutil.FailErr(t, "read handler body", err)
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/example", body),
	)

	if readsBeforeHandler != 0 {
		t.Fatalf("request reads before handler = %d", readsBeforeHandler)
	}
}

type countingReader struct {
	source io.Reader
	reads  int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	return r.source.Read(p)
}

func TestHTTPDebugMiddlewareWithholdsOversizeBodiesBeforeRedaction(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	t.Setenv("LYCAON_HTTP_DEBUG_MAX_BYTES", "64")
	CloseHTTPDebug()

	requestBody := `{"content":"` + strings.Repeat("request-body-", 32) + `"}`
	responseBody := `{"draft":"` + strings.Repeat("response-body-", 32) + `"}`
	handler := HTTPDebugMiddleware(HTTPBodyCapturePolicy{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		testutil.FailErr(t, "read handler body", err)
		if string(body) != requestBody {
			t.Fatalf("handler body changed")
		}
		_, _ = w.Write([]byte(responseBody))
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/v1/projects/p1/editor-documents/d1", strings.NewReader(requestBody))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read log", err)
	var entry httpDebugEntry
	testutil.FailErr(t, "decode log", json.Unmarshal(data[:len(data)-1], &entry))
	if entry.RequestBody != oversizeRequestBody || entry.ResponseBody != oversizeCaptureBody {
		t.Fatalf("captured bodies = %q / %q", entry.RequestBody, entry.ResponseBody)
	}
	if entry.RequestBytes != int64(len(requestBody)) || entry.ResponseBytes != int64(len(responseBody)) {
		t.Fatalf("captured sizes = %d / %d", entry.RequestBytes, entry.ResponseBytes)
	}
	if !entry.Truncated {
		t.Fatal("expected truncated capture")
	}
}

func TestHTTPDebugMiddlewareDetectsAResponseThatCrossesTheCapInAnotherWrite(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	t.Setenv("LYCAON_HTTP_DEBUG_MAX_BYTES", "8")
	CloseHTTPDebug()

	handler := HTTPDebugMiddleware(HTTPBodyCapturePolicy{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("12345678"))
		_, _ = w.Write([]byte("9"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/example", nil))

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read log", err)
	var entry httpDebugEntry
	testutil.FailErr(t, "decode log", json.Unmarshal(data[:len(data)-1], &entry))
	if entry.ResponseBody != oversizeCaptureBody || entry.ResponseBytes != 9 || !entry.Truncated {
		t.Fatalf("response capture = %q bytes=%d truncated=%v", entry.ResponseBody, entry.ResponseBytes, entry.Truncated)
	}
}

func TestRedactHTTPBodyCoversEverySecretFieldName(t *testing.T) {
	for _, name := range SecretFieldNames() {
		body := `{"provider":"anthropic","` + name + `":"sk-live-must-not-appear"}`
		got := RedactCaptureText(body)
		if strings.Contains(got, "sk-live-must-not-appear") {
			t.Errorf("RedactCaptureText left %q in cleartext: %s", name, got)
		}
		if !strings.Contains(got, `"provider":"anthropic"`) {
			t.Errorf("RedactCaptureText for %q destroyed non-secret context: %s", name, got)
		}
	}
}

func TestRedactHTTPBodyRedactsRealCredentialPayloads(t *testing.T) {
	for _, body := range []string{
		`{"api_key":"sk-live-must-not-appear"}`,
		`{"access_token":"sk-live-must-not-appear","refresh_token":"sk-live-must-not-appear"}`,
		`{"client_secret":"sk-live-must-not-appear","token_url":"https://example.com"}`,
		`{"headers":{"X-Api-Key":"sk-live-must-not-appear"}}`,
		`{"nested":{"credential":"sk-live-must-not-appear"}}`,
	} {
		if got := RedactCaptureText(body); strings.Contains(got, "sk-live-must-not-appear") {
			t.Errorf("RedactCaptureText(%s) = %s", body, got)
		}
	}
}

func TestHTTPDebugMiddlewareStructurallyRedactsCredentialRequestBodies(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	CloseHTTPDebug()
	// This value is absent from the runtime catalog.
	const credential = "9f2c1ab84de74100b6cbe4d3aa71f0e2"

	var received string
	carriesCredential := func(method, path string) bool {
		return method == http.MethodPost && path == "/v1/projects/p1/secrets"
	}
	handler := HTTPDebugMiddleware(HTTPBodyCapturePolicy{CredentialRequest: carriesCredential})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/projects/p1/secrets",
		strings.NewReader(`{"name":"n","purpose":"p","secret_value":"`+credential+`"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !strings.Contains(received, credential) {
		t.Fatalf("handler did not receive the body: %q", received)
	}
	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read file", err)
	if strings.Contains(string(data), credential) {
		t.Fatalf("credential body leaked: %s", data)
	}
	var entry httpDebugEntry
	testutil.FailErr(t, "decode log entry", json.Unmarshal(data[:len(data)-1], &entry))
	var captured map[string]any
	testutil.FailErr(t, "decode captured request", json.Unmarshal([]byte(entry.RequestBody), &captured))
	for _, field := range []string{"name", "purpose", "secret_value"} {
		if captured[field] != redacted {
			t.Errorf("captured %s = %v", field, captured[field])
		}
	}
}

func TestHTTPDebugMiddlewareRedactsCredentialBeforeSizeCap(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	t.Setenv("LYCAON_HTTP_DEBUG_MAX_BYTES", "64")
	CloseHTTPDebug()
	credential := strings.Repeat("opaque-value-", 64)

	carriesCredential := func(method, path string) bool {
		return method == http.MethodPost && path == "/v1/projects/p1/secrets"
	}
	handler := HTTPDebugMiddleware(HTTPBodyCapturePolicy{CredentialRequest: carriesCredential})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/projects/p1/secrets",
		strings.NewReader(`{"name":"context","secret_value":"`+credential+`"}`))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read file", err)
	if strings.Contains(string(data), credential) || strings.Contains(string(data), credential[:64]) {
		t.Fatalf("credential was truncated before redaction: %s", data)
	}
	var entry httpDebugEntry
	testutil.FailErr(t, "decode log entry", json.Unmarshal(data[:len(data)-1], &entry))
	if entry.RequestBody != oversizeRequestBody || entry.RequestBytes <= 64 {
		t.Fatalf("oversize credential request = %q (%d bytes)", entry.RequestBody, entry.RequestBytes)
	}
}

func TestHTTPDebugMiddlewareCategoricallyWithholdsCredentialResponseBodies(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	CloseHTTPDebug()
	const credential = "opaque-response-credential-42"

	carriesCredential := func(method, path string) bool {
		return method == http.MethodPost && strings.HasSuffix(path, "/reveal-challenges/challenge-1")
	}
	handler := HTTPDebugMiddleware(HTTPBodyCapturePolicy{WithholdResponse: carriesCredential})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"secret_value":"` + credential + `","version":2}`))
		}),
	)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/v1/projects/p1/secrets/s1/reveal-challenges/challenge-1", strings.NewReader(`{"signature":"safe"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), credential) {
		t.Fatalf("handler response was changed: %q", rec.Body.String())
	}
	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read file", err)
	if strings.Contains(string(data), credential) {
		t.Fatalf("credential response leaked: %s", data)
	}
	if strings.Contains(string(data), `\"version\":2`) || !strings.Contains(string(data), withheldResponseBody) {
		t.Fatalf("log did not withhold the response body: %s", data)
	}
}

func TestHTTPDebugMiddlewareCapturesComposerRequests(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	CloseHTTPDebug()
	const text = "summarize the selected records"
	var received string
	handler := HTTPDebugMiddleware(HTTPBodyCapturePolicy{})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			received = string(body)
			w.WriteHeader(http.StatusAccepted)
		}),
	)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/s1/prompts",
		strings.NewReader(`{"text":"`+text+`"}`))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(received, text) {
		t.Fatalf("handler did not receive composer text: %q", received)
	}
	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read file", err)
	if !strings.Contains(string(data), text) {
		t.Fatalf("composer request was not captured: %s", data)
	}
}

func TestHTTPDebugMiddlewareFailsClosedForMalformedCredentialJSON(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "http.jsonl")
	t.Setenv("LYCAON_HTTP_DEBUG", "1")
	t.Setenv("LYCAON_HTTP_DEBUG_FILE", logPath)
	CloseHTTPDebug()
	const credential = "opaque-malformed-credential"

	carriesCredential := func(method, path string) bool {
		return method == http.MethodPost && path == "/v1/projects/p1/secrets"
	}
	handler := HTTPDebugMiddleware(HTTPBodyCapturePolicy{CredentialRequest: carriesCredential})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }),
	)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/projects/p1/secrets",
		strings.NewReader(`{"secret_value":"`+credential))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read file", err)
	if strings.Contains(string(data), credential) {
		t.Fatalf("malformed credential body leaked: %s", data)
	}
	if !strings.Contains(string(data), invalidCredentialBody) {
		t.Fatalf("missing malformed credential marker: %s", data)
	}
}

// Route declarations cover credential fields without sensitive names.
func TestDeclaredCredentialBodyRedactsFieldsWithNoNeedleName(t *testing.T) {
	body := []byte(`{"code":"authorization-code-abc123","state":"csrf-state-xyz"}`)

	undeclared := redactHTTPBody(body, false)
	if !strings.Contains(undeclared, "authorization-code-abc123") {
		t.Fatalf("precondition failed: name matching already redacts code: %s", undeclared)
	}

	declared := redactHTTPBody(body, true)
	if strings.Contains(declared, "authorization-code-abc123") || strings.Contains(declared, "csrf-state-xyz") {
		t.Fatalf("declared credential body was captured in plaintext: %s", declared)
	}
	if !strings.Contains(declared, `"code"`) || !strings.Contains(declared, `"state"`) {
		t.Fatalf("declared credential body lost its shape: %s", declared)
	}
}

// Keys remain while every leaf value is redacted.
func TestDeclaredCredentialBodyRedactsEveryLeafShape(t *testing.T) {
	body := []byte(`{"outer":{"pin":1234,"nested":["one","two"],"flag":true,"absent":null}}`)
	declared := redactHTTPBody(body, true)
	for _, leaked := range []string{"1234", "one", "two", "true"} {
		if strings.Contains(declared, leaked) {
			t.Fatalf("declared credential body kept leaf %q: %s", leaked, declared)
		}
	}
	if !strings.Contains(declared, `"nested"`) || !strings.Contains(declared, `"pin"`) {
		t.Fatalf("declared credential body lost its shape: %s", declared)
	}
}
