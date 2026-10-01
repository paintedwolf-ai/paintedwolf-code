//go:build scanstress

// Package scanstress exercises the maintained OpenGrep engine under contention,
// cancellation, timeout, and host shutdown. It is a diagnostic harness: every
// assertion names an observable host fact (process table, temp tree, report
// bytes) rather than a wall-clock guess.
package scanstress

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// procRow is one line of the host process table.
type procRow struct {
	PID     int
	PPID    int
	PGID    int
	RSSKiB  int
	Command string
}

// processTable snapshots every process visible to this user.
func processTable(t *testing.T) map[int]procRow {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,pgid=,rss=,command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	rows := make(map[int]procRow, 512)
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		pgid, err3 := strconv.Atoi(fields[2])
		rss, err4 := strconv.Atoi(fields[3])
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		rows[pid] = procRow{PID: pid, PPID: ppid, PGID: pgid, RSSKiB: rss, Command: strings.Join(fields[4:], " ")}
	}
	return rows
}

// descendants returns every process reachable from root through parent links.
// Only processes this test started can appear; no other agent's tree is read
// for action.
func descendants(table map[int]procRow, root int) []procRow {
	children := make(map[int][]procRow, len(table))
	for _, row := range table {
		children[row.PPID] = append(children[row.PPID], row)
	}
	var out []procRow
	queue := []int{root}
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if seen[child.PID] {
				continue
			}
			seen[child.PID] = true
			out = append(out, child)
			queue = append(queue, child.PID)
		}
	}
	return out
}

// engineProcesses filters a descendant list to the scanner's own processes.
// The command text is diagnostic only; membership is established by the
// parent-link walk above.
func engineProcesses(rows []procRow) []procRow {
	var out []procRow
	for _, row := range rows {
		if strings.Contains(row.Command, "opengrep") || strings.Contains(row.Command, "sandbox-exec") {
			out = append(out, row)
		}
	}
	return out
}

// treeSample is one observation of this process and everything under it.
type treeSample struct {
	At            time.Time
	Processes     int
	EngineProcs   int
	AggregateRSS  int // KiB, self + descendants
	SelfRSS       int
	DistinctPGIDs int
}

// observer samples the process tree until stopped.
type observer struct {
	mu      sync.Mutex
	samples []treeSample
	runDirs map[string]bool
	stop    chan struct{}
	done    chan struct{}
}

func startObserver(t *testing.T, interval time.Duration) *observer {
	t.Helper()
	o := &observer{runDirs: map[string]bool{}, stop: make(chan struct{}), done: make(chan struct{})}
	self := os.Getpid()
	go func() {
		defer close(o.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			table := processTable(t)
			kids := descendants(table, self)
			sample := treeSample{At: time.Now(), Processes: len(kids), SelfRSS: table[self].RSSKiB}
			sample.AggregateRSS = table[self].RSSKiB
			pgids := map[int]bool{}
			for _, row := range kids {
				sample.AggregateRSS += row.RSSKiB
				pgids[row.PGID] = true
			}
			sample.EngineProcs = len(engineProcesses(kids))
			sample.DistinctPGIDs = len(pgids)
			o.mu.Lock()
			o.samples = append(o.samples, sample)
			for _, dir := range runDirsFromRows(kids) {
				o.runDirs[dir] = true
			}
			o.mu.Unlock()
			select {
			case <-o.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return o
}

func (o *observer) finish() []treeSample {
	close(o.stop)
	<-o.done
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]treeSample(nil), o.samples...)
}

// seenRunDirs returns every engine run directory this observer attributed to
// the current process while sampling.
func (o *observer) seenRunDirs() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.runDirs))
	for dir := range o.runDirs {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

func peak(samples []treeSample) treeSample {
	var best treeSample
	for _, s := range samples {
		if s.AggregateRSS > best.AggregateRSS {
			best = s
		}
	}
	return best
}

func peakProcs(samples []treeSample) int {
	best := 0
	for _, s := range samples {
		if s.Processes > best {
			best = s.Processes
		}
	}
	return best
}

// awaitNoEngineProcesses waits for every engine descendant of this process to
// leave the process table, returning the time it took and what survived.
func awaitNoEngineProcesses(t *testing.T, within time.Duration) (time.Duration, []procRow) {
	t.Helper()
	self := os.Getpid()
	start := time.Now()
	deadline := start.Add(within)
	var last []procRow
	for {
		last = engineProcesses(descendants(processTable(t), self))
		if len(last) == 0 {
			return time.Since(start), nil
		}
		if time.Now().After(deadline) {
			return time.Since(start), last
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// runDirsFromRows returns the engine run directories named by the given
// processes. The driver passes --output <runDir>/report.json, so argv names the
// exact directory that scan owns. Attribution comes from the parent-link walk
// that produced these rows; the flag only locates the path.
func runDirsFromRows(rows []procRow) []string {
	seen := map[string]bool{}
	for _, row := range rows {
		fields := strings.Fields(row.Command)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] != "--output" {
				continue
			}
			dir := filepath.Dir(fields[i+1])
			if strings.Contains(filepath.Base(dir), "paintedwolf-opengrep-") {
				seen[dir] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for dir := range seen {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

// survivingDirs reports which of the given paths still exist.
func survivingDirs(paths []string) []string {
	var out []string
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			out = append(out, path)
		}
	}
	return out
}

// loadAverage reports the host one-minute load, so a latency number carries the
// contention it was measured under. Other agents share this machine.
func loadAverage(t *testing.T) float64 {
	t.Helper()
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return -1
	}
	fields := strings.Fields(strings.Trim(strings.TrimSpace(string(out)), "{}"))
	if len(fields) == 0 {
		return -1
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return -1
	}
	return v
}

func dirBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil //nolint:nilerr // a vanishing temp tree is the measured condition
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func reportf(t *testing.T, format string, args ...any) {
	t.Helper()
	line := fmt.Sprintf(format, args...)
	t.Log(line)
	if path := os.Getenv("SCANSTRESS_REPORT"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()
		_, _ = fmt.Fprintf(f, "%s | %s\n", t.Name(), line)
	}
}
