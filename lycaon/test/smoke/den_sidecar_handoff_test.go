package smoke_test

import (
	"encoding/binary"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/testutil"
)

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

	apiToken := "den-handoff-parent-token"
	dbPath := filepath.Join(t.TempDir(), "handoff.db")
	proc := exec.CommandContext(t.Context(), bin, "serve", "--db", dbPath)
	proc.Dir = moduleRoot
	proc.Env = append(withoutEnv(os.Environ(), "LYCAON_API_TOKEN"),
		"LYCAON_ADDR="+addr,
		startupprotocol.APITokenStdinEnv+"=1",
		"LYCAON_LLM_MOCK=1",
	)
	var parent *exec.Cmd
	var parentProcess *smokeProcess
	if runtime.GOOS != "windows" {
		parent = exec.CommandContext(t.Context(), "cat")
		pipe, err := parent.StdinPipe()
		testutil.FailErr(t, "open stand-in parent input", err)
		t.Cleanup(func() { _ = pipe.Close() })
		parentProcess = startSmokeProcess(t, parent)
		proc.Env = append(withoutEnv(proc.Env, app.ParentPIDEnv), app.ParentPIDEnv+"="+strconv.Itoa(parent.Process.Pid))
	}
	stdin, err := proc.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	child := startSmokeProcess(t, proc)
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(apiToken)))
	if _, err := stdin.Write(append(frame, apiToken...)); err != nil {
		t.Fatalf("write token frame: %v", err)
	}

	baseURL := "http://" + addr
	child.waitForHealth(t, baseURL)

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

	if parent != nil {
		testutil.FailErr(t, "kill stand-in parent", parent.Process.Kill())
		testutil.AwaitClosed(t, "the stand-in parent exits", parentProcess.done)
		requireStoreReleased(t, child, dbPath)
	}
}
