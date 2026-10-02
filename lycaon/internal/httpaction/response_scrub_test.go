package httpaction

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

const echoedCredential = "echoed-credential-9931"

// echoingServer answers with the credential it was shown, padded to the
// requested size.
func echoingServer(t *testing.T, padding int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Seen", r.Header.Get("Authorization"))
		body := `{"padding":"` + strings.Repeat("x", padding) + `","echo":"` + r.Header.Get("Authorization") + `"}`
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// resolvedBearer prepares a request whose bearer token is a managed secret.
func resolvedBearer(t *testing.T, server *httptest.Server, extra map[string]any) (Deps, *secretcap.Resolution, map[string]any) {
	t.Helper()
	service, _ := managedRequestService(t)
	meta := hostSecret(t, service, "create", "API key", echoedCredential)
	args := map[string]any{
		"url": server.URL, "auth": map[string]any{"scheme": "bearer", "token": meta.Reference},
		"capability_request": loopbackCapability(t, server.URL),
	}
	for key, value := range extra {
		args[key] = value
	}
	resolved, err := service.Resolve(t.Context(), args, secretcap.ResolveContext{ProjectID: testdbseed.DefaultProjectID, ToolName: "http_request", ToolCallID: "call-1"})
	testutil.FailErr(t, "resolve request", err)
	t.Cleanup(func() { resolved.Finish(context.Background()) })
	deps := Deps{Boundary: testBoundary(), SecretMatcher: testSecretMatcher(t),
		SecretAsk: func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
			return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
		}}
	return deps, resolved, args
}

func assertScrubbed(t *testing.T, where string, content string, reference string) {
	t.Helper()
	if strings.Contains(content, echoedCredential) {
		t.Fatalf("%s carries the echoed credential", where)
	}
	if !strings.Contains(content, reference) {
		t.Fatalf("%s does not read the echo as its reference", where)
	}
}

// An oversized body lands under host data before the result is built, so the
// echo must already read as its reference when it lands.
func TestOversizeResponseEchoingAResolvedSecretLandsScrubbed(t *testing.T) {
	server := echoingServer(t, maxInlineBody)
	deps, resolved, args := resolvedBearer(t, server, nil)
	hostData := t.TempDir()
	tctx := sessionContext(t.TempDir(), "call-1")
	tctx.HostDataDir, tctx.CanonicalArgs, tctx.Secrets = hostData, args, resolved

	got, err := runRequest(t, deps, resolved.Arguments, tctx)
	testutil.FailErr(t, "oversize echo", err)
	if got.BodySpillPath == "" || !got.BodyRedacted {
		t.Fatalf("result = %+v, want a landed body reported as redacted", got)
	}
	reference := args["auth"].(map[string]any)["token"].(string)
	scopeRel, ok := tooloutput.AgentWireSpillScopeRel(hostData, got.BodySpillPath)
	if !ok {
		t.Fatalf("body_spill_path %q does not resolve as a host-data read", got.BodySpillPath)
	}
	stored, readErr := os.ReadFile(tooloutput.DiskPath(hostData, scopeRel))
	testutil.FailErr(t, "read landed body", readErr)
	landed, decodeErr := zstdcodec.Decompress(stored)
	testutil.FailErr(t, "decompress landed body", decodeErr)
	assertScrubbed(t, "the landed body", string(landed), reference)
	for _, header := range got.Headers {
		if header.Name == "X-Seen" {
			assertScrubbed(t, "the response header", header.Value, reference)
		}
	}
}

// A body the caller asked to keep in the project is scrubbed the same way.
func TestResponsePathEchoingAResolvedSecretIsScrubbed(t *testing.T) {
	server := echoingServer(t, 16)
	deps, resolved, args := resolvedBearer(t, server, map[string]any{"response_path": "echo.json"})
	root := t.TempDir()
	tctx := sessionContext(root, "call-1")
	tctx.CanonicalArgs, tctx.Secrets = args, resolved

	got, err := runRequest(t, deps, resolved.Arguments, tctx)
	testutil.FailErr(t, "landed echo", err)
	if !got.Written || got.ResponsePath != "echo.json" || !got.BodyRedacted {
		t.Fatalf("result = %+v, want the landed file reported as redacted", got)
	}
	content, readErr := os.ReadFile(filepath.Join(root, "echo.json"))
	testutil.FailErr(t, "read landed file", readErr)
	assertScrubbed(t, "the landed file", string(content), args["auth"].(map[string]any)["token"].(string))
}

// A response that echoes nothing is reported as received.
func TestResponseWithoutAnEchoIsNotReportedAsRedacted(t *testing.T) {
	server := bodyServer(t, "application/json", []byte(`{"ok":true}`))
	deps, resolved, args := resolvedBearer(t, server, nil)
	tctx := sessionContext(t.TempDir(), "call-1")
	tctx.CanonicalArgs, tctx.Secrets = args, resolved
	got, err := runRequest(t, deps, resolved.Arguments, tctx)
	testutil.FailErr(t, "plain response", err)
	if got.BodyRedacted || got.Body != `{"ok":true}` {
		t.Fatalf("result = %+v, want the body as received", got)
	}
}
