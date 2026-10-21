package smoke_test

import (
	"encoding/binary"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/startupprotocol"
)

// withoutEnv drops every entry for name from env.
func withoutEnv(env []string, key string) []string {
	out := env[:0:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, key+"=") {
			out = append(out, entry)
		}
	}
	return out
}

// The parent supplies a token over the startup pipe and an ephemeral port.
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

	// The desktop sends the bearer as the first frame on the engine's private
	// stdin, never in its environment.
	apiToken := "den-handoff-parent-token"
	proc := exec.CommandContext(t.Context(), bin, "serve", "--db", filepath.Join(t.TempDir(), "handoff.db"))
	proc.Dir = moduleRoot
	proc.Env = append(withoutEnv(os.Environ(), "LYCAON_API_TOKEN"),
		"LYCAON_ADDR="+addr,
		startupprotocol.APITokenStdinEnv+"=1",
		"LYCAON_LLM_MOCK=1",
	)
	stdin, err := proc.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	if err := proc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = proc.Process.Kill()
		_ = proc.Wait()
	})
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(apiToken)))
	if _, err := stdin.Write(append(frame, apiToken...)); err != nil {
		t.Fatalf("write token frame: %v", err)
	}

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
