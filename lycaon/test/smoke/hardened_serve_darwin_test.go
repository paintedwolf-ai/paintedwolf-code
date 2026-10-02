package smoke_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Release builds run the engine and its document core under the hardened
// runtime with no executable-memory entitlement, which kills a process the
// first time it executes generated code. An ad-hoc signature with the same
// options enforces the same rule, so editing a file proves the shipped
// arrangement on any Mac.
func TestHardenedEngineEditsADocument(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping smoke test in short mode")
	}
	core := os.Getenv("LYCAON_DOCUMENT_CORE_BINARY")
	if core == "" {
		t.Fatal("LYCAON_DOCUMENT_CORE_BINARY names no document core; run through ./task")
	}
	install := t.TempDir()
	engine := hardenedCopy(t, sharedServeBin, filepath.Join(install, "pw"))
	hardenedCopy(t, core, filepath.Join(install, "pw-document-core"))

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	testutil.FailErr(t, "reserve port", err)
	addr := ln.Addr().String()
	testutil.FailErr(t, "release port", ln.Close())

	const token = "lycaon-hardened-smoke-token"
	proc := exec.CommandContext(t.Context(), engine, "serve", "--db", filepath.Join(t.TempDir(), "smoke.db"))
	proc.Dir = smokeModuleRoot
	// The engine finds its core beside itself, as an installed app does.
	proc.Env = append(withoutEnv(os.Environ(), "LYCAON_DOCUMENT_CORE_BINARY"),
		"LYCAON_ADDR="+addr, "LYCAON_API_TOKEN="+token, "LYCAON_LLM_MOCK=1")
	var stderr bytes.Buffer
	proc.Stderr = &stderr
	testutil.FailErr(t, "start hardened engine", proc.Start())
	exited := make(chan error, 1)
	go func() { exited <- proc.Wait() }()
	t.Cleanup(func() {
		_ = proc.Process.Kill()
		<-exited
	})
	baseURL := "http://" + addr
	waitForHealth(t, baseURL)

	projectDir := t.TempDir()
	testutil.FailErr(t, "write project file", os.WriteFile(filepath.Join(projectDir, "notes.txt"), []byte("before\n"), 0o600))
	var project api.Project
	hardenedCall(t, baseURL, token, http.MethodPost, "/v1/projects", map[string]any{"roots": []map[string]string{{"path": projectDir}}}, http.StatusCreated, &project)
	if len(project.Roots) != 1 {
		t.Fatalf("project roots = %d, want 1", len(project.Roots))
	}
	var opened api.EditorDocument
	hardenedCall(t, baseURL, token, http.MethodPost, "/v1/projects/"+project.ID+"/editor-documents", api.OpenEditorDocumentRequest{
		Path: "notes.txt", RootID: project.Roots[0].ID, ClientID: "window:hardened-smoke",
	}, http.StatusCreated, &opened)
	var edited api.EditorDocument
	hardenedCall(t, baseURL, token, http.MethodPut, "/v1/projects/"+project.ID+"/editor-documents/"+opened.ID, api.ReplaceEditorDocumentRequest{
		OperationID: uuid.NewString(), ClientID: "window:hardened-smoke", ExpectedRevision: opened.Revision,
		Content: "after\n", EOL: "lf",
	}, http.StatusOK, &edited)
	if edited.Revision <= opened.Revision || !edited.Dirty {
		t.Fatalf("edited document revision %d (dirty %t), want a dirty revision past %d", edited.Revision, edited.Dirty, opened.Revision)
	}
	select {
	case err := <-exited:
		t.Fatalf("hardened engine exited during the edit: %v\n%s", err, stderr.String())
	default:
	}
	waitForHealth(t, baseURL)
}

func hardenedCopy(t *testing.T, source, target string) string {
	t.Helper()
	raw, err := os.ReadFile(source)
	testutil.FailErr(t, "read "+filepath.Base(source), err)
	testutil.FailErr(t, "copy "+filepath.Base(target), os.WriteFile(target, raw, 0o755))
	if out, err := exec.CommandContext(t.Context(), "codesign", "--force", "--sign", "-", "--options", "runtime", target).CombinedOutput(); err != nil {
		t.Fatalf("sign %s with the hardened runtime: %v: %s", filepath.Base(target), err, out)
	}
	return target
}

func hardenedCall(t *testing.T, baseURL, token, method, path string, body any, want int, out any) {
	t.Helper()
	raw, err := json.Marshal(body)
	testutil.FailErr(t, "encode "+path, err)
	req, err := http.NewRequestWithContext(t.Context(), method, baseURL+path, bytes.NewReader(raw))
	testutil.FailErr(t, "build "+path, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, method+" "+path, err)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	testutil.FailErr(t, "read "+path, err)
	if resp.StatusCode != want {
		t.Fatalf("%s %s status = %d, want %d: %s", method, path, resp.StatusCode, want, payload)
	}
	testutil.FailErr(t, "decode "+path, json.Unmarshal(payload, out))
}

func withoutEnv(env []string, name string) []string {
	kept := env[:0:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, name+"=") {
			kept = append(kept, entry)
		}
	}
	return kept
}
