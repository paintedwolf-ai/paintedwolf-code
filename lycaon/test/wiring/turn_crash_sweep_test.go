package wiring

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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

// storeDump is every row of every table keyed by its primary key, read
// without production code.
type storeDump map[string]map[string]string

func dumpStore(t *testing.T, path string) storeDump {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	testutil.FailErr(t, "open store dump", err)
	defer func() { _ = database.Close() }()
	dump := storeDump{}
	for _, table := range storeTables(t, database) {
		// Index segments are checked by integrity_check, not compared.
		if ftsShadow.MatchString(table) {
			continue
		}
		dump[table] = tableRows(t, database, table)
	}
	return dump
}

func storeTables(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `SELECT name FROM sqlite_schema WHERE type = 'table' AND sql NOT LIKE 'CREATE VIRTUAL%' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	testutil.FailErr(t, "list tables", err)
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var name string
		testutil.FailErr(t, "scan table", rows.Scan(&name))
		tables = append(tables, name)
	}
	testutil.FailErr(t, "finish tables", rows.Err())
	return tables
}

func primaryKey(t *testing.T, database *sql.DB, table string) []string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, table)
	testutil.FailErr(t, "primary key "+table, err)
	defer func() { _ = rows.Close() }()
	var key []string
	for rows.Next() {
		var name string
		testutil.FailErr(t, "scan key "+table, rows.Scan(&name))
		key = append(key, name)
	}
	testutil.FailErr(t, "finish key "+table, rows.Err())
	return key
}

func tableRows(t *testing.T, database *sql.DB, table string) map[string]string {
	t.Helper()
	key := primaryKey(t, database, table)
	rows, err := database.QueryContext(t.Context(), `SELECT * FROM "`+table+`"`)
	testutil.FailErr(t, "read "+table, err)
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	testutil.FailErr(t, "columns "+table, err)
	out := map[string]string{}
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
		id := string(encoded)
		if len(key) > 0 {
			keyed := make([]any, len(key))
			for i, column := range key {
				keyed[i] = row[column]
			}
			raw, err := json.Marshal(keyed)
			testutil.FailErr(t, "encode key "+table, err)
			id = string(raw)
		}
		out[id] = string(encoded)
	}
	testutil.FailErr(t, "finish "+table, rows.Err())
	return out
}

func diffDumps(before, after storeDump) []string {
	var out []string
	for _, table := range slices.Sorted(maps.Keys(before)) {
		for _, key := range slices.Sorted(maps.Keys(before[table])) {
			if row, ok := after[table][key]; !ok {
				out = append(out, "- "+table+" "+before[table][key])
			} else if row != before[table][key] {
				out = append(out, "~ "+table+" "+before[table][key]+" -> "+row)
			}
		}
		for _, key := range slices.Sorted(maps.Keys(after[table])) {
			if _, ok := before[table][key]; !ok {
				out = append(out, "+ "+table+" "+after[table][key])
			}
		}
	}
	return out
}

// reclaimedTables hold rows that recovery or delivery may remove after a
// crash: each removal is the row's purpose being fulfilled.
var reclaimedTables = map[string]string{
	"event_outbox":    "a delivered event leaves the outbox; delivery is checked separately",
	"llm_cost_totals": "an aggregate keyed by call status: settling an abandoned call moves its count and drops the empty bucket",
}

// bootVolatile are the rows every boot rewrites.
var bootVolatile = map[string]string{
	"store_meta": "store_revision counts boots",
}

// crashRun is one store a child turn left behind, read before and after the
// two boots.
type crashRun struct {
	crashed, boot1, boot2 storeDump
	prompt                []promptLine
	recovered             []string
}

var settledEntry = regexp.MustCompile(`msg="startup recovery settled" entry=(\S+)`)

// readCrashRun reads what the boot child wrote for the store on dir.
func readCrashRun(t *testing.T, dir string, boot crashChild) crashRun {
	t.Helper()
	if boot.code != 0 {
		t.Fatalf("boot child exited %d\n%s", boot.code, boot.output)
	}
	var run crashRun
	readCrashJSON(t, filepath.Join(dir, crashKilled), &run.crashed)
	readCrashJSON(t, filepath.Join(dir, crashBoot1), &run.boot1)
	readCrashJSON(t, filepath.Join(dir, crashBoot2), &run.boot2)
	readCrashJSON(t, filepath.Join(dir, crashPrompt), &run.prompt)
	for _, match := range settledEntry.FindAllStringSubmatch(boot.output, -1) {
		run.recovered = append(run.recovered, match[1])
	}
	requireStoreIntegrity(t, filepath.Join(dir, crashStore))
	return run
}

// crashPoint is one commit point's kill and the two boots after it.
type crashPoint struct {
	dir        string
	kill, boot crashChild
	err        error
}

// killAndBoot runs every point's child processes, a bounded number at once;
// each point's checks run afterwards in its own subtest.
func killAndBoot(t *testing.T, points []string) []crashPoint {
	t.Helper()
	runs := make([]crashPoint, len(points))
	slots := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for i, point := range points {
		runs[i].dir = t.TempDir()
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			run := &runs[i]
			if run.kill, run.err = runCrashChild("turn", run.dir, point); run.err != nil || run.kill.code != crashExit {
				return
			}
			run.boot, run.err = runCrashChild("boot", run.dir, "")
		})
	}
	wg.Wait()
	return runs
}

func requireStoreIntegrity(t *testing.T, path string) {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	testutil.FailErr(t, "open store for integrity", err)
	defer func() { _ = database.Close() }()
	var verdict string
	testutil.FailErr(t, "integrity check", database.QueryRowContext(t.Context(), `PRAGMA integrity_check`).Scan(&verdict))
	if verdict != "ok" {
		t.Fatalf("integrity check: %s", verdict)
	}
	rows, err := database.QueryContext(t.Context(), `PRAGMA foreign_key_check`)
	testutil.FailErr(t, "foreign key check", err)
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign key check found a dangling reference")
	}
	testutil.FailErr(t, "finish foreign key check", rows.Err())
}

func rowsOf(t *testing.T, dump storeDump, table string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range dump[table] {
		var row map[string]any
		testutil.FailErr(t, "decode "+table, json.Unmarshal([]byte(raw), &row))
		out = append(out, row)
	}
	return out
}

// checkCrashRun holds a crashed store to P1 against the uninterrupted reference.
func checkCrashRun(t *testing.T, run, reference crashRun) {
	t.Helper()
	for table, rows := range run.crashed {
		if _, ok := reclaimedTables[table]; ok {
			continue
		}
		for key := range rows {
			if _, ok := run.boot1[table][key]; !ok {
				t.Errorf("acknowledged %s row %s is gone after boot", table, key)
			}
		}
	}
	for _, event := range rowsOf(t, run.crashed, "event_outbox") {
		if event["topic"] == "" || !json.Valid([]byte(event["data_json"].(string))) {
			t.Errorf("committed event %v cannot be delivered", event["event_id"])
		}
	}
	for _, dump := range []storeDump{run.boot1, run.boot2} {
		if queued := len(dump["event_outbox"]); queued != 0 {
			t.Errorf("%d committed events still queued after boot", queued)
		}
	}
	users := map[string]int{}
	results := map[string]int{}
	for _, msg := range rowsOf(t, run.boot2, "messages") {
		if msg["role"] == "user" {
			users[msg["content"].(string)]++
		}
		if id, _ := msg["tool_result_call_id"].(string); id != "" {
			results[id]++
		}
	}
	for content, count := range users {
		if count > 1 {
			t.Errorf("user boundary %q stored %d times", content, count)
		}
	}
	for id, count := range results {
		if count > 1 {
			t.Errorf("tool call %s has %d results", id, count)
		}
	}
	for _, attempt := range rowsOf(t, run.boot2, "turn_attempts") {
		if attempt["status"] == "running" {
			t.Errorf("turn attempt %v still running after boot", attempt["id"])
		}
	}
	finalizing := map[any]bool{}
	for _, attempt := range rowsOf(t, run.crashed, "turn_attempts") {
		if attempt["status"] == "running" && attempt["phase"] == "finalizing" {
			finalizing[attempt["turn_id"]] = true
		}
	}
	for _, turn := range rowsOf(t, run.boot2, "turns") {
		switch {
		case turn["status"] == "running":
			t.Errorf("turn %v still running after boot", turn["id"])
		case finalizing[turn["id"]] && turn["status"] != "complete":
			t.Errorf("turn %v committed its finalizing checkpoint but is %v after boot", turn["id"], turn["status"])
		}
	}
	changed := diffDumps(withoutVolatile(run.boot1), withoutVolatile(run.boot2))
	if len(changed) > 0 {
		t.Errorf("second boot changed the store:\n%s", strings.Join(changed, "\n"))
	}
	if !promptSettledPrefix(run.prompt, reference.prompt) {
		t.Errorf("prompt after recovery is not the uninterrupted prompt up to its settled point:\n got %+v\nwant prefix of %+v", run.prompt, reference.prompt)
	}
}

func withoutVolatile(dump storeDump) storeDump {
	out := maps.Clone(dump)
	for table := range bootVolatile {
		delete(out, table)
	}
	return out
}

// promptSettledPrefix holds a recovered prompt to the uninterrupted one line
// by line. Two departures follow from an unsettled phase: a call no result
// answers yet is left out of its assistant line, and a call whose effect was in
// flight is answered by the host's interruption result.
func promptSettledPrefix(got, want []promptLine) bool {
	if len(got) > len(want) {
		return false
	}
	answered := map[string]bool{}
	for _, line := range got {
		if line.ToolCallID != "" {
			answered[line.ToolCallID] = true
		}
	}
	for i, line := range got {
		expected := want[i]
		if line.Role == "assistant" {
			expected.ToolCalls = slices.DeleteFunc(slices.Clone(expected.ToolCalls), func(call wire.ToolCall) bool { return !answered[call.ID] })
			if len(line.ToolCalls) == 0 && len(expected.ToolCalls) == 0 {
				line.ToolCalls, expected.ToolCalls = nil, nil
			}
		}
		if line.Role == "tool" && line.ToolCallID == expected.ToolCallID && slices.Contains(line.Codes, "TOOL_OWNER_INTERRUPTED") {
			continue
		}
		a, _ := json.Marshal(line)
		b, _ := json.Marshal(expected)
		if string(a) != string(b) {
			return false
		}
	}
	return true
}

// Killed at any commit of a turn and booted twice, the store keeps every
// acknowledged row, fences the turn, stays consistent, and the second boot
// changes nothing (invariant P1).
func TestTurnSurvivesKillAtEveryCommitPoint(t *testing.T) {
	switch os.Getenv(crashModeEnv) {
	case "turn":
		runTurnChild(t, os.Getenv(crashDirEnv), os.Getenv(crashAtEnv))
		return
	case "boot":
		runBootChild(t, os.Getenv(crashDirEnv))
		return
	}
	dir := t.TempDir()
	clean, err := runCrashChild("turn", dir, "")
	testutil.FailErr(t, "uninterrupted turn", err)
	if clean.code != finishedExit {
		t.Fatalf("uninterrupted turn exited %d\n%s", clean.code, clean.output)
	}
	points := readCommitPoints(t, dir)
	boot, err := runCrashChild("boot", dir, "")
	testutil.FailErr(t, "boot uninterrupted store", err)
	reference := readCrashRun(t, dir, boot)
	checkCrashRun(t, reference, reference)
	slices.Sort(reference.recovered)
	t.Logf("%d commit points:\n%s", len(points), strings.Join(points, "\n"))
	t.Logf("%d recovery entries settled over two boots:\n%s", len(reference.recovered), strings.Join(slices.Compact(reference.recovered), "\n"))
	requirePinnedCommitPoints(t, points)

	runs := killAndBoot(t, points)
	for i, point := range points {
		t.Run(point, func(t *testing.T) {
			run := runs[i]
			testutil.FailErr(t, "run children", run.err)
			if run.kill.code != crashExit {
				t.Fatalf("turn child exited %d, not at the commit point\n%s", run.kill.code, run.kill.output)
			}
			checkCrashRun(t, readCrashRun(t, run.dir, run.boot), reference)
		})
	}
}

// requirePinnedCommitPoints fails when the turn gains, loses or reshapes a
// commit, so each new commit point joins the sweep deliberately.
func requirePinnedCommitPoints(t *testing.T, points []string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "turn-commit-points.txt"))
	testutil.FailErr(t, "read pinned commit points", err)
	pinned := strings.Split(strings.TrimSpace(string(raw)), "\n")
	got := slices.Sorted(slices.Values(points))
	slices.Sort(pinned)
	if !slices.Equal(got, pinned) {
		t.Fatalf("turn commit points changed; review each and update testdata/turn-commit-points.txt to:\n%s", strings.Join(got, "\n"))
	}
}

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
	crashKilled  = "killed.json"
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
	c.seen[label]++
	point := label + " #" + strconv.Itoa(c.seen[label])
	if point == c.killAt {
		os.Exit(crashExit)
	}
	_, _ = fmt.Fprintln(c.log, point)
	c.mu.Unlock()
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
	// The turn ends with the session's idle commit, after Prompt returns.
	WaitForSessionIdleGate(t, h, t.Context(), sess.ID)
	waitForOutboxDrained(t, h)
	tracer.armed.Store(false)
	os.Exit(finishedExit)
}

// runBootChild boots the host left on dir twice, as the engine's next two
// starts would, and writes the store after each and the prompt after the second.
func runBootChild(t *testing.T, dir string) {
	writeCrashJSON(t, filepath.Join(dir, crashKilled), dumpStore(t, filepath.Join(dir, crashStore)))
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

// Generated identifiers differ between the reference run and a crashed one.
var runIdentity = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// promptLine is the model-facing part of one assembled history message.
type promptLine struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCalls  []wire.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Codes      []string        `json:"codes,omitempty"`
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
		content := runIdentity.ReplaceAllString(strings.ReplaceAll(msg.Content, dir, "<dir>"), "<id>")
		lines[i] = promptLine{Role: string(msg.Role), Content: content, ToolCalls: msg.ToolCalls}
		if msg.ToolResult != nil {
			lines[i].ToolCallID = msg.ToolResult.ToolCallID
			lines[i].Codes = msg.ToolResult.Codes
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

// crashChild is one finished child process.
type crashChild struct {
	code   int
	output string
}

// runCrashChild runs this binary in mode on dir; it is safe to call from any
// goroutine.
func runCrashChild(mode, dir, killAt string) (crashChild, error) {
	child := exec.Command(os.Args[0], "-test.run=^TestTurnSurvivesKillAtEveryCommitPoint$", "-test.count=1", "-test.v")
	child.Env = append(os.Environ(), crashModeEnv+"="+mode, crashDirEnv+"="+dir, crashAtEnv+"="+killAt)
	output, err := child.CombinedOutput()
	var exited *exec.ExitError
	switch {
	case err == nil:
		return crashChild{output: string(output)}, nil
	case errors.As(err, &exited):
		return crashChild{code: exited.ExitCode(), output: string(output)}, nil
	}
	return crashChild{}, fmt.Errorf("%s child at %q: %w\n%s", mode, killAt, err, output)
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
