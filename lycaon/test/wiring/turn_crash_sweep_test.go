package wiring

import (
	"database/sql"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
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
