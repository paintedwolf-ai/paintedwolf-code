package httpaction

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func testSecrets(t *testing.T) (*secretcap.Service, *[]secretmatch.Remembered) {
	t.Helper()
	service, remembered, _ := newTestSecrets(t)
	return service, remembered
}

// testSecretsWithStore hands back the database so a test can take the managed
// secret store away mid-request.
func testSecretsWithStore(t *testing.T) (*secretcap.Service, *db.Store) {
	t.Helper()
	service, _, database := newTestSecrets(t)
	return service, database
}

func newTestSecrets(t *testing.T) (*secretcap.Service, *[]secretmatch.Remembered, *db.Store) {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "root-1", testdbseed.DefaultProjectID)
	values := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "test managed secret",
	}, func(string) bool { return true })
	remembered := &[]secretmatch.Remembered{}
	return secretcap.NewWithStore(database, values, func(_ string, items []secretmatch.Remembered) {
		*remembered = append(*remembered, items...)
	}), remembered, database
}

func sessionContext(root, call string) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID,
			ProjectID:  testdbseed.DefaultProjectID,
			SessionID:  "root-1",
			ToolCallID: call},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "root"},
	}
}

// result is what a test reads from one exchange through Perform, the path curl
// runs.
type result struct {
	Status      int
	FinalURL    string
	Headers     []outboundhttp.Header
	SentHeaders []outboundhttp.Header
	Redirects   []outboundhttp.Hop
	Bytes       int64
	SHA256      string
	ContentType string
	Body        string
	Listener    *api.LoopbackListenerFact
}

func runRequest(t *testing.T, deps Deps, args map[string]any, tctx tools.ToolContext) (result, error) {
	t.Helper()
	return runRequestWith(t, deps, args, tctx, ExchangeOptions{})
}

// runRequestWith runs Perform with frontend options, keeping the whole body.
func runRequestWith(t *testing.T, deps Deps, args map[string]any, tctx tools.ToolContext, opts ExchangeOptions) (result, error) {
	t.Helper()
	ctx, scope := socketref.WithScope(t.Context())
	defer scope.Close()
	opts.MaxBodyBytes = 4 << 20
	ex, err := Perform(ctx, deps, args, tctx, opts)
	var transport *TransportFailure
	if errors.As(err, &transport) {
		return result{}, transport.Reject
	}
	if err != nil {
		return result{}, err
	}
	return resultOf(ex), nil
}

func resultOf(ex Exchange) result {
	resp := ex.Response
	return result{
		Status: resp.Status, FinalURL: ex.FinalURL, Headers: resp.Headers, SentHeaders: ex.SentHeaders,
		Redirects: ex.Redirects, Bytes: resp.Bytes, SHA256: resp.SHA256, ContentType: resp.ContentType,
		Body: string(resp.Body), Listener: ex.Listener,
	}
}

// registerPerform serves http_request through Perform in a test registry, so
// executor-level tests reach the exchange core curl uses.
func registerPerform(registry *tools.DefaultRegistry, deps Deps) error {
	return registry.Register("http_request", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		ex, err := Perform(ctx, deps, args, tctx, ExchangeOptions{MaxBodyBytes: 4 << 20})
		var transport *TransportFailure
		if errors.As(err, &transport) {
			return "", transport.Reject
		}
		if err != nil {
			return "", err
		}
		out, err := json.Marshal(resultOf(ex))
		return string(out), err
	})
}

func fmtResult(t *testing.T, got result) string {
	t.Helper()
	encoded, err := json.Marshal(got)
	testutil.FailErr(t, "encode result", err)
	return string(encoded)
}

func TestHTTPRequestAuthAndFormReachTheServer(t *testing.T) {
	approveDials(t)
	var seen struct {
		auth, query, contentType, field, filename, fileBody string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.auth = r.Header.Get("Authorization")
		seen.query = r.URL.RawQuery
		seen.contentType = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			seen.field = r.FormValue("version")
			if file, header, err := r.FormFile("package"); err == nil {
				data, _ := io.ReadAll(file)
				seen.filename, seen.fileBody = header.Filename, string(data)
			}
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	testutil.FailErr(t, "write crate", os.WriteFile(filepath.Join(root, "dist.crate"), []byte("crate-bytes"), 0o600))

	got, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL + "/api/v1/crates/new?existing=1", "method": "PUT",
		"auth":               map[string]any{"scheme": "basic", "username": "admin", "password": "pass:word"},
		"form":               []any{map[string]any{"name": "version", "value": "1.2.3"}, map[string]any{"name": "package", "path": "dist.crate", "content_type": "application/gzip"}},
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	testutil.FailErr(t, "multipart request", err)
	if got.Status != http.StatusCreated {
		t.Fatalf("status = %d", got.Status)
	}
	if seen.auth != "Basic YWRtaW46cGFzczp3b3Jk" {
		t.Fatalf("authorization = %q", seen.auth)
	}
	if seen.query != "existing=1" {
		t.Fatalf("query = %q", seen.query)
	}
	if !strings.HasPrefix(seen.contentType, "multipart/form-data") || seen.field != "1.2.3" || seen.filename != "dist.crate" || seen.fileBody != "crate-bytes" {
		t.Fatalf("form = %+v", seen)
	}

	bearer, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL + "/", "auth": map[string]any{"scheme": "bearer", "token": "tok-1"},
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-2"))
	testutil.FailErr(t, "bearer request", err)
	if bearer.Status != http.StatusCreated || seen.auth != "Bearer tok-1" {
		t.Fatalf("bearer authorization = %q", seen.auth)
	}

	for _, args := range []map[string]any{
		{"url": server.URL, "auth": map[string]any{"scheme": "basic", "username": "a:b", "password": "x"}},
		{"url": server.URL, "auth": map[string]any{"scheme": "digest"}},
		{"url": server.URL, "auth": map[string]any{"scheme": "bearer", "token": "t"}, "headers": []any{map[string]any{"name": "Authorization", "value": "x"}}},
		{"url": server.URL, "method": "POST", "form": []any{map[string]any{"name": "a", "value": "1", "path": "b"}}},
		{"url": server.URL, "method": "POST", "form": []any{map[string]any{"name": "a", "value": "1"}}, "body_text": "x"},
	} {
		_, err := runRequest(t, Deps{Boundary: testBoundary()}, args, sessionContext(root, "call-3"))
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("args %v error = %v, want TOOL_ARGS_INVALID", args, err)
		}
	}
}

func TestHTTPRequestSentHeadersIncludeTheHostAddedContentType(t *testing.T) {
	approveDials(t)
	var seen struct {
		method, contentType, body string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method = r.Method
		seen.contentType = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		seen.body = string(data)
		w.Header().Set("X-Custom-Resp", "ok")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()

	got, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url":                server.URL + "/oauth/token",
		"method":             "POST",
		"form":               []any{map[string]any{"name": "grant_type", "value": "client_credentials"}},
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	testutil.FailErr(t, "form request", err)
	if got.Status != http.StatusOK {
		t.Fatalf("status = %d", got.Status)
	}
	if !strings.HasPrefix(seen.contentType, "multipart/form-data; boundary=") || !strings.Contains(seen.body, "client_credentials") {
		t.Fatalf("content-type = %q body = %q", seen.contentType, seen.body)
	}
	foundContentType := false
	for _, h := range got.SentHeaders {
		if strings.EqualFold(h.Name, "Content-Type") && h.Value == seen.contentType {
			foundContentType = true
		}
	}
	if !foundContentType {
		t.Fatalf("SentHeaders did not contain Content-Type: %+v", got.SentHeaders)
	}
}

// approveAll stands in for the approval gate, which admits these loopback
// requests under their capability. Every attributed dial reaches it.
func approveAll(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
	return true
}

func TestMain(m *testing.M) {
	confine.SetEgressResolver(approveAll)
	os.Exit(m.Run())
}

// restoreResolver puts the package's approving gate back after a test that
// installed its own.
func restoreResolver() { confine.SetEgressResolver(approveAll) }

// approveDials makes a test independent of whatever an earlier test left
// installed.
func approveDials(t *testing.T) {
	t.Helper()
	restoreResolver()
	t.Cleanup(restoreResolver)
}
