package wiring

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"modernc.org/sqlite"
)

const (
	crashDirEnv  = "PW_TURN_CRASH_DIR"
	crashAtEnv   = "PW_TURN_CRASH_AT"
	crashExit    = 75
	finishedExit = 76
	crashStore   = "wiring-test.db"
	crashLog     = "commits.log"
	crashSession = "session-id"
)

// commitTracer names each commit on one store file by the table operations it
// carried and that label's occurrence since arming, and can exit the process
// inside the commit hook so the commit never becomes durable.
type commitTracer struct {
	path   string
	killAt string
	armed  atomic.Bool
	mu     sync.Mutex
	seen   map[string]int
	log    *os.File
}

var preUpdateOps = map[int32]string{9: "delete", 18: "insert", 23: "update"}

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
		// Hooks on one connection run on that connection's goroutine in turn.
		ops := map[string]bool{}
		hooks.RegisterPreUpdateHook(func(d sqlite.SQLitePreUpdateData) {
			ops[d.TableName+":"+preUpdateOps[d.Op]] = true
		})
		hooks.RegisterRollbackHook(func() { clear(ops) })
		hooks.RegisterCommitHook(func() int32 {
			label := slices.Sorted(func(yield func(string) bool) {
				for op := range ops {
					if !yield(op) {
						return
					}
				}
			})
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

// runCrashChild drives one turn on dir and exits: at the named commit point, or
// with finishedExit once the turn and its event delivery have settled.
func runCrashChild(t *testing.T, dir, killAt string) {
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

func startCrashChild(t *testing.T, dir, killAt string) int {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestTurnSurvivesKillAtEveryCommitPoint$", "-test.count=1")
	child.Env = append(os.Environ(), crashDirEnv+"="+dir, crashAtEnv+"="+killAt)
	output, err := child.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		t.Fatalf("crash child at %q: %v\n%s", killAt, err, output)
	}
	if code := exited.ExitCode(); code != crashExit && code != finishedExit {
		t.Fatalf("crash child at %q exited %d\n%s", killAt, code, output)
	}
	return exited.ExitCode()
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

// waitForOutboxDrained returns once delivery has emptied the outbox; the
// test deadline bounds it.
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

// storeDump is every row of every table, read without production code.
type storeDump map[string][]string

func dumpStore(t *testing.T, path string) storeDump {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	testutil.FailErr(t, "open store dump", err)
	defer func() { _ = database.Close() }()
	rows, err := database.QueryContext(t.Context(), `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	testutil.FailErr(t, "list tables", err)
	var tables []string
	for rows.Next() {
		var name string
		testutil.FailErr(t, "scan table", rows.Scan(&name))
		tables = append(tables, name)
	}
	testutil.FailErr(t, "finish tables", rows.Err())
	testutil.FailErr(t, "close tables", rows.Close())
	dump := storeDump{}
	for _, table := range tables {
		dump[table] = tableRows(t, database, table)
	}
	return dump
}

func tableRows(t *testing.T, database *sql.DB, table string) []string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `SELECT * FROM "`+table+`"`)
	if err != nil {
		// Virtual tables whose module this connection lacks are not store rows.
		return nil
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	testutil.FailErr(t, "columns "+table, err)
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		testutil.FailErr(t, "scan "+table, rows.Scan(targets...))
		row := map[string]any{}
		for i, column := range columns {
			if raw, ok := values[i].([]byte); ok {
				values[i] = string(raw)
			}
			row[column] = values[i]
		}
		encoded, err := json.Marshal(row)
		testutil.FailErr(t, "encode "+table, err)
		out = append(out, string(encoded))
	}
	testutil.FailErr(t, "finish "+table, rows.Err())
	slices.Sort(out)
	return out
}

func diffDumps(before, after storeDump) []string {
	var out []string
	for _, table := range slices.Sorted(func(yield func(string) bool) {
		for name := range before {
			if !yield(name) {
				return
			}
		}
	}) {
		for _, row := range before[table] {
			if !slices.Contains(after[table], row) {
				out = append(out, "- "+table+" "+row)
			}
		}
		for _, row := range after[table] {
			if !slices.Contains(before[table], row) {
				out = append(out, "+ "+table+" "+row)
			}
		}
	}
	return out
}

// bootCrashedStore opens the host a crash left on dir, runs startup recovery
// and closes it.
func bootCrashedStore(t *testing.T, dir string) {
	t.Helper()
	t.Run("boot", func(t *testing.T) {
		t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
		h := BuildForTestAt(t, dir, WithLLMClient(crashTurnScript()))
		stop := h.StartBackgroundWorkers(t, t.Context())
		waitForOutboxDrained(t, h)
		stop()
	})
}

func TestTurnSurvivesKillAtEveryCommitPoint(t *testing.T) {
	if dir := os.Getenv(crashDirEnv); dir != "" {
		runCrashChild(t, dir, os.Getenv(crashAtEnv))
		return
	}
	reference := t.TempDir()
	if code := startCrashChild(t, reference, ""); code != finishedExit {
		t.Fatalf("clean run exited %d", code)
	}
	points := readCommitPoints(t, reference)
	t.Logf("%d commit points:\n%s", len(points), strings.Join(points, "\n"))
	afterRun := dumpStore(t, filepath.Join(reference, crashStore))
	bootCrashedStore(t, reference)
	boot1 := dumpStore(t, filepath.Join(reference, crashStore))
	bootCrashedStore(t, reference)
	boot2 := dumpStore(t, filepath.Join(reference, crashStore))
	t.Logf("boot 1 changes:\n%s", strings.Join(diffDumps(afterRun, boot1), "\n"))
	t.Logf("boot 2 changes:\n%s", strings.Join(diffDumps(boot1, boot2), "\n"))
}
