//go:build unix

package exec

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Signal relays one signal to the running tree without the terminate path's
// escalation: a stage that traps TERM keeps running, reports it, and exits
// only when KILL ends it; the stage then reports 128 plus the signal.
func TestAsyncPipelineSignalRelaysWithoutTerminating(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready.pid")
	stdout := &lockedBuffer{}
	script := `trap 'echo got-term' TERM; echo $$ > ` + ready + `; while :; do sleep 0.05; done`
	run, err := StartPipelineAsync(context.Background(), []Stage{{Name: "sh", Args: []string{"-c", script}}}, ExecOpts{Launch: HostLaunch("exec test")}, stdout, nil)
	testutil.FailErr(t, "start async", err)
	waitForPIDFile(t, ready)
	if !run.Signal(syscall.SIGTERM) {
		t.Fatal("TERM reached no running stage")
	}
	for !strings.Contains(stdout.String(), "got-term") {
		select {
		case <-run.Done():
			t.Fatalf("a relayed TERM terminated the trapping stage: %q", stdout.String())
		case <-t.Context().Done():
			t.Fatal("the stage never reported TERM")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !run.Signal(syscall.SIGKILL) {
		t.Fatal("KILL reached no running stage")
	}
	result, _ := run.Wait()
	if result == nil || len(result.Stages) != 1 || result.Stages[0].ExitStatus() != 128+int(syscall.SIGKILL) {
		t.Fatalf("killed stage result = %+v", result)
	}
	if run.Signal(syscall.SIGTERM) {
		t.Fatal("a settled pipeline reported a delivery")
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestStageRunReportsSignalStatusAndProcessorTime(t *testing.T) {
	result, _ := RunPipeline(context.Background(), []Stage{{Name: "sh", Args: []string{"-c", `i=0; while [ $i -lt 20000 ]; do i=$((i+1)); done; kill -INT $$`}}}, ExecOpts{Launch: HostLaunch("exec test"), Dir: t.TempDir()})
	if result == nil || len(result.Stages) != 1 {
		t.Fatalf("result = %+v", result)
	}
	stage := result.Stages[0]
	if stage.ExitStatus() != 128+int(syscall.SIGINT) {
		t.Fatalf("signaled stage status = %d, want %d", stage.ExitStatus(), 128+int(syscall.SIGINT))
	}
	if stage.UserTime+stage.SystemTime <= 0 {
		t.Fatalf("reaped stage reported no processor time: %+v", stage)
	}
}
