package smoke_test

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The parent supplies a token and ephemeral port to the sidecar.
func TestDenSidecarHandoffBearer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping smoke test in short mode")
	}

	moduleRoot := smokeModuleRoot
	bin := sharedServeBin

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	apiToken := "den-handoff-parent-token"
	proc := exec.CommandContext(t.Context(), bin, "serve", "--db", filepath.Join(t.TempDir(), "handoff.db"))
	proc.Dir = moduleRoot
	proc.Env = append(os.Environ(),
		"LYCAON_ADDR="+addr,
		"LYCAON_API_TOKEN="+apiToken,
		"LYCAON_LLM_MOCK=1",
	)
	if err := proc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = proc.Process.Kill()
		_ = proc.Wait()
	})

	baseURL := "http://" + addr
	waitForHealth(t, baseURL)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/projects", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("projects: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/projects status = %d, want 200", resp.StatusCode)
	}
}
