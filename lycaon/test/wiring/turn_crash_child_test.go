package wiring

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"modernc.org/sqlite"
)

// The sweep re-executes this test binary as a child in one of two modes.
const (
	crashModeEnv = "PW_TURN_CRASH_MODE"
	crashDirEnv  = "PW_TURN_CRASH_DIR"
	crashAtEnv   = "PW_TURN_CRASH_AT"
	crashExit    = 75
	finishedExit = 76
	crashStore   = "wiring-test.db"
	crashLog     = "commits.log"
	crashSession = "session-id"
	crashBoot1   = "boot1.json"
	crashBoot2   = "boot2.json"
	crashPrompt  = "prompt.json"
)

// commitTracer names each commit on one store file by the table operations it
// carried and that label's occurrence since arming, and can exit the process
// inside the commit hook, before the commit is durable.
type commitTracer struct {
	path   string
	killAt string
	armed  atomic.Bool
	mu     sync.Mutex
	seen   map[string]int
	log    *os.File
}

var preUpdateOps = map[int32]string{9: "delete", 18: "insert", 23: "update"}

// Full-text shadow tables change with segment merges, not with the write.
var ftsShadow = regexp.MustCompile(`_fts_(data|idx|docsize|config|content)$`)

func installCommitTracer(t *testing.T, path, killAt, logPath string) *commitTracer {
	t.Helper()
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	testutil.FailErr(t, "open commit log", err)
	c := &commitTracer{path: path, killAt: killAt, seen: map[string]int{}, log: log}
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
		u, err := url.Parse(dsn)
		if err != nil || filepath.Clean(u.Path) != c.path {
			return nil
		}
		hooks, ok := conn.(sqlite.HookRegisterer)
		if !ok {
			return errors.New("sqlite connection cannot register hooks")
		}
		// One connection runs its hooks one at a time.
		ops := map[string]bool{}
		hooks.RegisterPreUpdateHook(func(d sqlite.SQLitePreUpdateData) {
			if !ftsShadow.MatchString(d.TableName) {
				ops[d.TableName+":"+preUpdateOps[d.Op]] = true
			}
		})
		hooks.RegisterRollbackHook(func() { clear(ops) })
		hooks.RegisterCommitHook(func() int32 {
			label := slices.Sorted(mapsKeys(ops))
			clear(ops)
			if len(label) > 0 && c.armed.Load() {
				c.commit(strings.Join(label, " "))
			}
			return 0
		})
		return nil
	})
	return c
}

func mapsKeys(m map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func (c *commitTracer) commit(label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen[label]++
	point := label + " #" + strconv.Itoa(c.seen[label])
	if point == c.killAt {
		os.Exit(crashExit)
	}
	_, _ = fmt.Fprintln(c.log, point)
}

// crashTurnScript is one model output with a tool call, then the closing answer.
func crashTurnScript() *stackScriptLLM {
	return &stackScriptLLM{steps: []stackStep{commandStep("Reading the notes.", "cat notes.txt")}}
}

// runTurnChild drives one turn on dir and exits: at the named commit point, or
// with finishedExit once the turn and its event delivery have settled.
func runTurnChild(t *testing.T, dir, killAt string) {
	tracer := installCommitTracer(t, filepath.Join(dir, crashStore), killAt, filepath.Join(dir, crashLog))
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	h := BuildForTestAt(t, dir, WithLLMClient(crashTurnScript()))
	project := h.ProjectDir(t, "project")
	testutil.FailErr(t, "seed notes", os.WriteFile(filepath.Join(project, "notes.txt"), []byte("first note\nsecond note\n"), 0o644))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, project)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, t.Context(), sess.ID)
	testutil.FailErr(t, "record session", os.WriteFile(filepath.Join(dir, crashSession), []byte(sess.ID), 0o600))
	tracer.armed.Store(true)
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "Read the notes.")
	testutil.FailErr(t, "prompt", err)
	waitForOutboxDrained(t, h)
	tracer.armed.Store(false)
	os.Exit(finishedExit)
}

// runBootChild boots the host left on dir twice, as the engine's next two
// starts would, and writes the store after each and the prompt after the second.
func runBootChild(t *testing.T, dir string) {
	// Settled recovery entries are logged at debug.
	t.Setenv("LYCAON_LOG_LEVEL", "debug")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	boot := func(name, dump string) {
		t.Run(name, func(t *testing.T) {
			h := BuildForTestAt(t, dir, WithLLMClient(crashTurnScript()))
			stop := h.StartBackgroundWorkers(t, t.Context())
			waitForOutboxDrained(t, h)
			stop()
			if dump == crashBoot2 {
				writeCrashJSON(t, filepath.Join(dir, crashPrompt), assembledPrompt(t, h, dir))
			}
		})
		writeCrashJSON(t, filepath.Join(dir, dump), dumpStore(t, filepath.Join(dir, crashStore)))
	}
	boot("first", crashBoot1)
	boot("second", crashBoot2)
}

// promptLine is the model-facing part of one assembled history message.
type promptLine struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCalls  []wire.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

func assembledPrompt(t *testing.T, h *Harness, dir string) []promptLine {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, crashSession))
	testutil.FailErr(t, "read session id", err)
	sess, err := h.Store.Get(t.Context(), string(raw))
	testutil.FailErr(t, "read session", err)
	history, err := h.Store.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read history", err)
	assembled, _ := promptassembly.Assemble(sess, history, promptassembly.Config{})
	lines := make([]promptLine, len(assembled))
	for i, msg := range assembled {
		lines[i] = promptLine{Role: string(msg.Role), Content: strings.ReplaceAll(msg.Content, dir, "<dir>"), ToolCalls: msg.ToolCalls}
		if msg.ToolResult != nil {
			lines[i].ToolCallID = msg.ToolResult.ToolCallID
		}
	}
	return lines
}

func writeCrashJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	testutil.FailErr(t, "encode "+filepath.Base(path), err)
	testutil.FailErr(t, "write "+filepath.Base(path), os.WriteFile(path, raw, 0o600))
}

func readCrashJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+filepath.Base(path), err)
	testutil.FailErr(t, "decode "+filepath.Base(path), json.Unmarshal(raw, value))
}

// startCrashChild runs this binary in mode on dir and returns its exit code and output.
func startCrashChild(t *testing.T, mode, dir, killAt string) (int, string) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestTurnSurvivesKillAtEveryCommitPoint$", "-test.count=1", "-test.v")
	child.Env = append(os.Environ(), crashModeEnv+"="+mode, crashDirEnv+"="+dir, crashAtEnv+"="+killAt)
	output, err := child.CombinedOutput()
	var exited *exec.ExitError
	switch {
	case err == nil:
		return 0, string(output)
	case errors.As(err, &exited):
		return exited.ExitCode(), string(output)
	}
	t.Fatalf("%s child at %q: %v\n%s", mode, killAt, err, output)
	return 0, ""
}

func readCommitPoints(t *testing.T, dir string) []string {
	t.Helper()
	file, err := os.Open(filepath.Join(dir, crashLog))
	testutil.FailErr(t, "open commit log", err)
	defer func() { _ = file.Close() }()
	var points []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		points = append(points, scanner.Text())
	}
	testutil.FailErr(t, "read commit log", scanner.Err())
	return points
}

// waitForOutboxDrained returns once delivery has emptied the outbox; the test
// deadline bounds it.
func waitForOutboxDrained(t *testing.T, h *Harness) {
	t.Helper()
	for {
		var queued int
		testutil.FailErr(t, "count queued events", h.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM event_outbox`).Scan(&queued))
		if queued == 0 {
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatalf("outbox still holds %d events", queued)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
