//go:build !windows

package smoke_test

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A command whose tree holds a detached, SIGTERM-ignoring daemon is running
// and an event subscriber is connected when the engine is told to stop.
func TestServeStopsCleanlyUnderLoad(t *testing.T) {
	engine := startLoadedEngine(t)
	testutil.FailErr(t, "signal the engine", engine.process.Signal(syscall.SIGTERM))
	engine.requireLeftNothing(t)
}

func TestServeDiesWithItsParent(t *testing.T) {
	engine := startLoadedEngine(t)
	testutil.FailErr(t, "kill the engine's parent", engine.parent.Process.Kill())
	testutil.AwaitClosed(t, "the parent exits", engine.parentDone.done)
	engine.requireLeftNothing(t)
}

type loadedEngine struct {
	process    *os.Process
	done       *smokeProcess
	parent     *exec.Cmd
	parentDone *smokeProcess
	dbPath     string
	tmpDir     string
	owned      []osprocess.Incarnation
	subscriber <-chan struct{}
}

func startLoadedEngine(t *testing.T) *loadedEngine {
	t.Helper()
	testutil.SkipIfShort(t, "starts the real engine")
	token := uuid.NewString()
	work := t.TempDir()
	project := filepath.Join(work, "project")
	configRoot := filepath.Join(work, "engine-root")
	tmpDir := filepath.Join(work, "tmp")
	fixtures := filepath.Join(configRoot, config.ConfigDirName, "fixtures")
	for _, dir := range []string{project, fixtures, tmpDir} {
		testutil.FailErr(t, "create engine directory", os.MkdirAll(dir, 0o700))
	}
	daemonPID := filepath.Join(project, "daemon.pid")
	testutil.FailErr(t, "write daemon program", os.WriteFile(filepath.Join(project, "daemon.py"),
		[]byte(testutil.DetachedDaemonScript("daemon.pid", true)), 0o600))
	// The engine's mock model answers the prompt with one command that starts the daemon.
	mock := fmt.Sprintf("responses:\n  - pattern: \"lifecycle daemon\"\n    tool_calls:\n      - id: call_daemon\n"+
		"        name: command\n        args:\n          command: python3 daemon.py %s\n  - pattern: \".*\"\n    text: done\n", token)
	testutil.FailErr(t, "write mock model", os.WriteFile(filepath.Join(fixtures, "mock_llm.yaml"), []byte(mock), 0o600))

	parent := exec.CommandContext(t.Context(), "cat")
	parentInput, err := parent.StdinPipe()
	testutil.FailErr(t, "open stand-in parent input", err)
	t.Cleanup(func() { _ = parentInput.Close() })
	parentDone := startSmokeProcess(t, parent)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	testutil.FailErr(t, "reserve engine address", err)
	addr := ln.Addr().String()
	testutil.FailErr(t, "release engine address", ln.Close())
	apiToken := "lifecycle-" + token
	dbPath := filepath.Join(work, "store", "engine.db")
	testutil.FailErr(t, "create store directory", os.MkdirAll(filepath.Dir(dbPath), 0o700))
	proc := exec.CommandContext(t.Context(), sharedServeBin, "serve", "--db", dbPath)
	proc.Dir = smokeModuleRoot
	env := os.Environ()
	for _, name := range []string{"LYCAON_API_TOKEN", app.ParentPIDEnv, config.EnvConfigRoot, "TMPDIR"} {
		env = withoutEnv(env, name)
	}
	proc.Env = append(env,
		"LYCAON_ADDR="+addr,
		"LYCAON_LLM_MOCK=1",
		"LYCAON_CONFIG_DIR="+filepath.Join(work, "config"),
		config.EnvConfigRoot+"="+configRoot,
		"TMPDIR="+tmpDir,
		startupprotocol.APITokenStdinEnv+"=1",
		app.ParentPIDEnv+"="+strconv.Itoa(parent.Process.Pid),
	)
	stdin, err := proc.StdinPipe()
	testutil.FailErr(t, "open engine input", err)
	done := startSmokeProcess(t, proc)
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(apiToken)))
	_, err = stdin.Write(append(frame, apiToken...))
	testutil.FailErr(t, "hand the engine its bearer", err)

	baseURL := "http://" + addr
	done.waitForHealth(t, baseURL)
	projectID := createSmokeProject(t, baseURL, apiToken, project)
	sessionID := createSmokeSession(t, baseURL, apiToken, projectID)
	subscriber := openEventSubscriber(t, baseURL, apiToken, projectID)

	submitPrompt(t, baseURL, apiToken, sessionID, "start the lifecycle daemon")
	runApprovedCommand(t, baseURL, apiToken, sessionID, daemonPID)

	engine := &loadedEngine{process: proc.Process, done: done, parent: parent, parentDone: parentDone,
		dbPath: dbPath, tmpDir: tmpDir, subscriber: subscriber}
	engine.owned = ownedProcesses(t, proc.Process.Pid, token)
	return engine
}

func openEventSubscriber(t *testing.T, baseURL, apiToken, projectID string) <-chan struct{} {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/events?project_id="+projectID, nil)
	testutil.FailErr(t, "event subscription request", err)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "subscribe to events", err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("event subscription status = %d", resp.StatusCode)
	}
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
	}()
	return ended
}

// submitPrompt sends text once the session's workspace has finished preparing.
func submitPrompt(t *testing.T, baseURL, apiToken, sessionID, text string) {
	t.Helper()
	body := fmt.Sprintf(`{"operation_id":%q,"text":%q}`, uuid.NewString(), text)
	testutil.WaitFor(t, "the session accepts the prompt", func() bool {
		status, raw := smokeRequest(t, baseURL, apiToken, http.MethodPost, "/v1/sessions/"+sessionID+"/prompts", body)
		if status < 300 {
			return true
		}
		var refusal struct {
			Code string `json:"code"`
		}
		if status == http.StatusConflict && json.Unmarshal(raw, &refusal) == nil && refusal.Code == "session_preparing" {
			return false
		}
		t.Fatalf("submit the prompt: status = %d body = %s", status, raw)
		return false
	})
}

// runApprovedCommand approves the command when the host's policy asks, and
// returns once the daemon has written its pid.
func runApprovedCommand(t *testing.T, baseURL, apiToken, sessionID, pidPath string) {
	t.Helper()
	testutil.WaitFor(t, "the daemon reports its pid", func() bool {
		if _, err := os.Stat(pidPath); err == nil {
			return true
		}
		var list struct {
			Checkpoints []struct {
				ID           string `json:"id"`
				Status       string `json:"status"`
				ToolApproval *struct {
					Plan struct {
						Options []struct {
							ID string `json:"id"`
						} `json:"options"`
					} `json:"plan"`
				} `json:"tool_approval"`
			} `json:"checkpoints"`
		}
		smokeJSON(t, baseURL, apiToken, http.MethodGet, "/v1/sessions/"+sessionID+"/checkpoints", "", &list)
		for _, checkpoint := range list.Checkpoints {
			if checkpoint.Status == "pending" && checkpoint.ToolApproval != nil && len(checkpoint.ToolApproval.Plan.Options) > 0 {
				smokeJSON(t, baseURL, apiToken, http.MethodPost, "/v1/sessions/"+sessionID+"/checkpoints/"+checkpoint.ID,
					fmt.Sprintf(`{"kind":"tool_approval","action":"approve","option_id":%q}`, checkpoint.ToolApproval.Plan.Options[0].ID), nil)
			}
		}
		return false
	})
}

func smokeJSON(t *testing.T, baseURL, apiToken, method, path, body string, out any) {
	t.Helper()
	status, raw := smokeRequest(t, baseURL, apiToken, method, path, body)
	if status >= 300 {
		t.Fatalf("%s %s status = %d body = %s", method, path, status, raw)
	}
	if out != nil {
		testutil.FailErr(t, "decode "+path, json.Unmarshal(raw, out))
	}
}

func smokeRequest(t *testing.T, baseURL, apiToken, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, baseURL+path, strings.NewReader(body))
	testutil.FailErr(t, "build "+path+" request", err)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, method+" "+path, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	testutil.FailErr(t, "read "+path, err)
	return resp.StatusCode, raw
}

// ownedProcesses captures the engine's descendants and every process carrying
// the command's token, which finds the daemon after it left the tree.
func ownedProcesses(t *testing.T, enginePID int, token string) []osprocess.Incarnation {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "ps", "-axo", "pid=,ppid=,command=").Output()
	testutil.FailErr(t, "list processes", err)
	children := map[int][]int{}
	parents := map[int]int{}
	commands := map[int]string{}
	tokened := map[int]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		ppid, errParent := strconv.Atoi(fields[1])
		if errPID != nil || errParent != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		parents[pid] = ppid
		commands[pid] = strings.Join(fields[2:], " ")
		if strings.Contains(commands[pid], token) {
			tokened[pid] = true
		}
	}
	pids := map[int]bool{}
	for queue := []int{enginePID}; len(queue) > 0; queue = queue[1:] {
		for _, child := range children[queue[0]] {
			pids[child] = true
			queue = append(queue, child)
		}
	}
	for pid := range tokened {
		pids[pid] = true
	}
	sessions := map[int]int{}
	for pid := range pids {
		if sid, err := unix.Getsid(pid); err == nil {
			sessions[sid]++
		}
	}
	// The daemon's session leader has exited, so no other owned process shares it.
	detached := false
	for pid := range tokened {
		if sid, err := unix.Getsid(pid); err == nil && sessions[sid] == 1 {
			detached = true
		}
	}
	if !detached {
		var described []string
		for pid := range pids {
			sid, err := unix.Getsid(pid)
			described = append(described, fmt.Sprintf("%d(parent %d, session %d %v, token %t): %s", pid, parents[pid], sid, err, tokened[pid], commands[pid]))
		}
		t.Fatalf("no process carrying the command token runs in a session of its own:\n%s", strings.Join(described, "\n"))
	}
	var owned []osprocess.Incarnation
	for pid := range pids {
		if inc, ok := osprocess.IncarnationOf(pid); ok {
			owned = append(owned, inc)
		}
	}
	return owned
}

func (e *loadedEngine) requireLeftNothing(t *testing.T) {
	t.Helper()
	testutil.AwaitClosed(t, "the engine exits", e.done.done)
	testutil.FailErr(t, "engine exit", e.done.err)
	testutil.AwaitClosed(t, "the event subscriber's stream ends", e.subscriber)
	for _, inc := range e.owned {
		testutil.WaitFor(t, fmt.Sprintf("engine-owned process %d exits", inc.PID), inc.Ended)
	}
	wal, err := os.Stat(e.dbPath + "-wal")
	if err == nil && wal.Size() != 0 {
		t.Fatalf("shutdown left %d WAL bytes", wal.Size())
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect WAL after shutdown: %v", err)
	}
	stamps, err := filepath.Glob(filepath.Join(filepath.Dir(e.dbPath), "locks", "*.engine.pid"))
	testutil.FailErr(t, "list instance-lock stamps", err)
	if len(stamps) != 0 {
		t.Fatalf("shutdown left instance-lock stamps %v", stamps)
	}
	left, err := os.ReadDir(e.tmpDir)
	testutil.FailErr(t, "list engine temporary directory", err)
	if len(left) != 0 {
		t.Fatalf("shutdown left %d entries in the engine's temporary directory, first %s", len(left), left[0].Name())
	}
}
