package httpaction

import (
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
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
		Agent: tools.DefaultToolProfileID, ProjectID: testdbseed.DefaultProjectID,
		SessionID: "root-1", ToolCallID: call,
		Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root",
	}
}

func runRequest(t *testing.T, deps Deps, args map[string]any, tctx tools.ToolContext) (result, error) {
	t.Helper()
	registry := tools.NewDefaultRegistry()
	if err := Register(registry, deps); err != nil {
		t.Fatalf("register http_request: %v", err)
	}
	out, err := registry.Run(t.Context(), "http_request", args, tctx)
	if err != nil {
		return result{}, err
	}
	var got result
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return got, nil
}

func TestHTTPRequestCookieJarPersistsALoginSessionWithoutDisclosingValues(t *testing.T) {
	secrets, remembered := testSecrets(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-value-1", Path: "/", HttpOnly: true})
			w.WriteHeader(http.StatusNoContent)
		case "/me":
			cookie, err := r.Cookie("session")
			if err != nil || cookie.Value != "session-value-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"user":"admin"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	deps := Deps{Boundary: testBoundary(), Secrets: secrets}
	capability := loopbackCapability(t, server.URL)

	login, err := runRequest(t, deps, map[string]any{
		"url": server.URL + "/login", "method": "POST", "cookie_jar": "registry", "capability_request": capability,
	}, sessionContext(t.TempDir(), "call-1"))
	testutil.FailErr(t, "login request", err)
	if login.Cookies == nil || login.Cookies.Stored != 1 || login.Cookies.Sent != 0 || login.Cookies.Reference == "" {
		t.Fatalf("login cookies = %+v", login.Cookies)
	}
	for _, header := range login.Headers {
		if strings.EqualFold(header.Name, "Set-Cookie") {
			t.Fatalf("Set-Cookie leaked into the result: %+v", login.Headers)
		}
	}
	if strings.Contains(fmtResult(t, login), "session-value-1") {
		t.Fatal("cookie value leaked into the result")
	}
	found := false
	for _, item := range *remembered {
		if item.Secret == "session-value-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("cookie value was not admitted to screening")
	}

	me, err := runRequest(t, deps, map[string]any{
		"url": server.URL + "/me", "cookie_jar": "registry", "capability_request": capability,
	}, sessionContext(t.TempDir(), "call-2"))
	testutil.FailErr(t, "authenticated request", err)
	if me.Status != http.StatusOK || me.Cookies == nil || me.Cookies.Sent != 1 || me.Cookies.Reference != login.Cookies.Reference {
		t.Fatalf("authenticated response = %+v cookies=%+v", me, me.Cookies)
	}

	anonymous, err := runRequest(t, deps, map[string]any{
		"url": server.URL + "/me", "capability_request": capability,
	}, sessionContext(t.TempDir(), "call-3"))
	testutil.FailErr(t, "anonymous request", err)
	if anonymous.Status != http.StatusUnauthorized || anonymous.Cookies != nil {
		t.Fatalf("a request without a jar carried cookies: %+v", anonymous)
	}

	_, err = runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL + "/me", "cookie_jar": "registry", "capability_request": capability,
	}, sessionContext(t.TempDir(), "call-4"))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != CookieJarFailedCode {
		t.Fatalf("jar without a secret store = %v, want %s", err, CookieJarFailedCode)
	}
}

func fmtResult(t *testing.T, got result) string {
	t.Helper()
	encoded, err := json.Marshal(got)
	testutil.FailErr(t, "encode result", err)
	return string(encoded)
}

func TestHTTPRequestAuthQueryAndFormReachTheServer(t *testing.T) {
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
		"query":              []any{map[string]any{"name": "dry run", "value": "yes&no"}},
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
	// The URL keeps its own query bytes, and declared parameters follow in the
	// order they were given.
	if seen.query != "existing=1&dry+run=yes%26no" {
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
		{"url": server.URL, "method": "POST", "body_form": []any{map[string]any{"name": "a", "value": "1"}}, "body_text": "x"},
		{"url": server.URL, "method": "POST", "body_form": []any{map[string]any{"name": "a", "value": "1"}}, "form": []any{map[string]any{"name": "b", "value": "2"}}},
		{"url": server.URL, "query": []any{map[string]any{"name": "", "value": "1"}}},
	} {
		_, err := runRequest(t, Deps{Boundary: testBoundary()}, args, sessionContext(root, "call-3"))
		var reject *tools.ToolReject
		if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("args %v error = %v, want TOOL_ARGS_INVALID", args, err)
		}
	}
}

func TestHTTPRequestResponsePathStreamsPastTheInlineCap(t *testing.T) {
	size := int64(outboundhttp.DefaultBodyMax + 4096)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		chunk := make([]byte, 64<<10)
		for i := range chunk {
			chunk[i] = byte(i)
		}
		remaining := size
		for remaining > 0 {
			n := int64(len(chunk))
			if n > remaining {
				n = remaining
			}
			_, _ = w.Write(chunk[:n])
			remaining -= n
		}
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	got, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL + "/artifact.bin", "response_path": "dist/artifact.bin",
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	testutil.FailErr(t, "streamed download", err)
	if !got.Written || got.Bytes != size || got.SHA256 == "" || got.Body != "" {
		t.Fatalf("download receipt = %+v", got)
	}
	info, err := os.Stat(filepath.Join(root, "dist", "artifact.bin"))
	if err != nil || info.Size() != size {
		t.Fatalf("landed file = %v err=%v", info, err)
	}
}

func TestHTTPRequestSupportsBodyFormAndSentHeaders(t *testing.T) {
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
		"url":    server.URL + "/oauth/token",
		"method": "POST",
		"body_form": []any{
			map[string]any{"name": "grant_type", "value": "client_credentials"},
			map[string]any{"name": "scope", "value": "read write & admin"},
		},
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	testutil.FailErr(t, "body_form request", err)
	if got.Status != http.StatusOK {
		t.Fatalf("status = %d", got.Status)
	}
	if seen.contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("content-type = %q, want application/x-www-form-urlencoded", seen.contentType)
	}
	if seen.body != "grant_type=client_credentials&scope=read+write+%26+admin" {
		t.Fatalf("body = %q", seen.body)
	}
	foundContentType := false
	for _, h := range got.SentHeaders {
		if strings.EqualFold(h.Name, "Content-Type") && h.Value == "application/x-www-form-urlencoded" {
			foundContentType = true
		}
	}
	if !foundContentType {
		t.Fatalf("SentHeaders did not contain Content-Type: %+v", got.SentHeaders)
	}
}
