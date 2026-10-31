package session

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHelperBackgroundHTTPServer(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_SERVER") != "1" {
		return
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stdout, "LISTENING=http://%s\n", ln.Addr().String())
	_ = os.Stdout.Sync()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		msg := "SERVED:" + r.URL.RawQuery
		fmt.Fprintln(os.Stdout, msg)
		_ = os.Stdout.Sync()
		_, _ = io.WriteString(w, msg)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	_ = srv.Serve(ln)
	os.Exit(0)
}

func TestStopWorkerRuntimePreservesParentAndSibling(t *testing.T) {
	mgr, _ := newTestManager(t)
	parent, err := mgr.Coordinator.Context.Sessions.(Store).Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create worker parent", err)
	child, err := mgr.Coordinator.Context.Sessions.(Store).CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create target worker", err)
	sibling, err := mgr.Coordinator.Context.Sessions.(Store).CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "code-reviewer"})
	testutil.FailErr(t, "create sibling worker", err)
	for _, id := range []string{parent.ID, child.ID, sibling.ID} {
		testutil.FailErr(t, "mark active runtime", mgr.Coordinator.Context.Sessions.(Store).SetSessionStatus(t.Context(), id, api.SessionStatusBusy))
	}
	released := []string{}
	testutil.FailErr(t, "register process cleanup", mgr.Resources.RegisterCleanup("fixture-processes", 20, func(_ context.Context, id string) error {
		released = append(released, id)
		return nil
	}))
	if err := mgr.Workers.Cancellations.StopRuntime(t.Context(), parent.ID); err == nil {
		t.Fatal("accepted coordinator as worker")
	}
	if len(released) != 0 {
		t.Fatalf("released parent resources: %v", released)
	}
	testutil.FailErr(t, "stop parked worker resources", mgr.Workers.Cancellations.StopRuntime(t.Context(), child.ID))
	if len(released) != 1 || released[0] != child.ID {
		t.Fatalf("released resources=%v", released)
	}
	for _, id := range []string{parent.ID, child.ID, sibling.ID} {
		got, err := mgr.Coordinator.Context.Sessions.(Store).Get(t.Context(), id)
		testutil.FailErr(t, "inspect retained session", err)
		want := api.SessionStatusBusy
		if id == child.ID {
			want = api.SessionStatusIdle
		}
		if got.Status != want {
			t.Fatalf("session=%s status=%s want=%s", id, got.Status, want)
		}
	}
}

func TestStopAndDeleteLifecycleForBackgroundAndAwaitedCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	ctx := t.Context()
	mgr, st := newTestManager(t)
	completed := make(chan bgprocess.Completion, 16)
	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(cctx context.Context, c bgprocess.Completion) {
			mgr.Processes.HandleCommandCompletion(cctx, c)
			completed <- c
		},
	})
	t.Cleanup(func() { _ = reg.Lifecycle.Close(context.Background()) })
	mgr.SetBackgroundRegistry(reg)

	parent, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent session", err)

	exe, err := os.Executable()
	testutil.FailErr(t, "executable", err)
	runner := hostcmd.NewRunner()
	serverDir := t.TempDir()

	serverHandle, err := reg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: parent.ID, ProjectID: parent.ProjectID, Runner: runner, Mode: bgprocess.JobModeBackground,
		OriginTool: "command",
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("background http server test"),
			ProjectDir: serverDir,
			IOParams:   hostcmd.IOParams{InlineEnv: map[string]string{"GO_WANT_HELPER_SERVER": "1"}},
			Stages:     []exec.Stage{{Name: exe, Args: []string{"-test.run=^TestHelperBackgroundHTTPServer$"}}},
		},
	})
	testutil.FailErr(t, "start background server", err)

	var listenURL string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := reg.Output.ReadRawOutput(parent.ID, serverHandle, 0)
		testutil.FailErr(t, "read server startup output", err)
		var b strings.Builder
		for _, ch := range raw.Output.Chunks {
			b.WriteString(ch.Text)
		}
		sc := bufio.NewScanner(strings.NewReader(b.String()))
		for sc.Scan() {
			if after, ok := strings.CutPrefix(sc.Text(), "LISTENING="); ok {
				listenURL = strings.TrimSpace(after)
				break
			}
		}
		if listenURL != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if listenURL == "" {
		t.Fatal("background HTTP server did not report listening address")
	}

	// Simulate a subsequent turn with an unpromoted awaited command, then abort the turn.
	testutil.FailErr(t, "mark parent busy", st.SetSessionStatus(ctx, parent.ID, api.SessionStatusBusy))
	awaitedHandle, err := reg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: parent.ID, ProjectID: parent.ProjectID, Runner: runner, Mode: bgprocess.JobModeAwaited,
		OriginTool: "command",
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("awaited sleep test"),
			ProjectDir: serverDir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start awaited command", err)

	testutil.FailErr(t, "abort parent turn", mgr.Stops.Abort(ctx, parent.ID, "user stopped"))

	if _, err := reg.Snapshot(ctx, parent.ID, awaitedHandle, 0); err == nil {
		t.Fatal("expected unpromoted awaited command to be stopped and discarded on abort")
	}
	active := reg.ActiveJobs(parent.ID)
	if len(active) != 1 || active[0].Handle != serverHandle {
		t.Fatalf("active jobs after abort = %+v, want [%s]", active, serverHandle)
	}

	// Exercise the surviving background server over HTTP and verify new output is captured.
	resp, err := http.Get(listenURL + "/?probe=after-abort")
	testutil.FailErr(t, "GET surviving server after abort", err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	testutil.FailErr(t, "read response body", err)
	if string(body) != "SERVED:probe=after-abort" {
		t.Fatalf("server response = %q, want SERVED:probe=after-abort", string(body))
	}

	var sawProbe bool
	for time.Now().Before(time.Now().Add(2 * time.Second)) {
		raw, err := reg.Output.ReadRawOutput(parent.ID, serverHandle, 0)
		testutil.FailErr(t, "read server output after abort", err)
		for _, ch := range raw.Output.Chunks {
			if strings.Contains(ch.Text, "SERVED:probe=after-abort") {
				sawProbe = true
				break
			}
		}
		if sawProbe {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawProbe {
		t.Fatal("surviving server output after abort was not captured by registry")
	}

	// Verify both worker cancellation and worker completion stop worker background commands.
	cancelWorker, err := st.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create cancel worker", err)
	completeWorker, err := st.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create complete worker", err)

	cancelWorkerHandle, err := reg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: cancelWorker.ID, RootSessionID: parent.ID, ProjectID: parent.ProjectID,
		Runner: runner, Mode: bgprocess.JobModeBackground, OriginTool: "command",
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("worker cancel bg"),
			ProjectDir: serverDir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start cancel-worker bg", err)

	completeWorkerHandle, err := reg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: completeWorker.ID, RootSessionID: parent.ID, ProjectID: parent.ProjectID,
		Runner: runner, Mode: bgprocess.JobModeBackground, OriginTool: "command",
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("worker complete bg"),
			ProjectDir: serverDir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"29"}}},
		},
	})
	testutil.FailErr(t, "start complete-worker bg", err)

	testutil.FailErr(t, "cancel worker runtime", mgr.Workers.Cancellations.StopRuntime(ctx, cancelWorker.ID))
	if reg.HasRunning(cancelWorker.ID) {
		t.Fatalf("cancelled worker still has running background command %s", cancelWorkerHandle)
	}
	var gotCancelReason bgprocess.TerminationReason
	select {
	case c := <-completed:
		if c.Handle == cancelWorkerHandle {
			gotCancelReason = c.TerminationReason
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cancelled worker background process completion")
	}
	if gotCancelReason != bgprocess.TerminationWorkerCancelled {
		t.Fatalf("cancelled worker process reason = %q, want worker_cancelled", gotCancelReason)
	}

	testutil.FailErr(t, "dispose completed worker runtime",
		mgr.Chats.DisposeRuntime(bgprocess.WithTerminationReason(ctx, bgprocess.TerminationWorkerCompleted), completeWorker.ID))
	if reg.HasRunning(completeWorker.ID) {
		t.Fatalf("completed worker still has running background command %s", completeWorkerHandle)
	}
	var gotCompleteReason bgprocess.TerminationReason
	select {
	case c := <-completed:
		if c.Handle == completeWorkerHandle {
			gotCompleteReason = c.TerminationReason
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for completed worker background process completion")
	}
	if gotCompleteReason != bgprocess.TerminationWorkerCompleted {
		t.Fatalf("completed worker process reason = %q, want worker_completed", gotCompleteReason)
	}

	// Parent server must still be running after worker cancellation and completion.
	if !reg.HasRunning(parent.ID) {
		t.Fatal("parent background server died during worker teardown")
	}

	// Deleting the chat stops its background server with chat_deleted.
	testutil.FailErr(t, "delete parent chat", mgr.Chats.Delete(ctx, parent.ID))
	if reg.HasRunning(parent.ID) {
		t.Fatal("parent background server survived chat deletion")
	}
	var gotDeleteReason bgprocess.TerminationReason
	select {
	case c := <-completed:
		if c.Handle == serverHandle {
			gotDeleteReason = c.TerminationReason
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for deleted chat background server completion")
	}
	if gotDeleteReason != bgprocess.TerminationChatDeleted {
		t.Fatalf("deleted chat server reason = %q, want chat_deleted", gotDeleteReason)
	}
}
