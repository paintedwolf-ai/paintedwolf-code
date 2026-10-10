//go:build unix

package exec

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// reaperWriteTimeout bounds a registration so a stalled companion never blocks a launch.
const reaperWriteTimeout = time.Second

var reaper struct {
	mu   sync.Mutex
	pipe *os.File
}

// StartReaper launches the companion that kills every tracked child once this
// process exits by any path, SIGKILL included; macOS has no parent-death signal.
// Only this process holds the pipe's close-on-exec write end, so the kernel
// closing it at exit is the trigger. The companion leads its own process group,
// out of reach of signals sent to the engine's group.
func StartReaper(name string, args ...string) error {
	read, write, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("reaper pipe: %w", err)
	}
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // G204 — the engine's own executable; the reaper outlives every request
	cmd.Stdin = read
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = read.Close()
		_ = write.Close()
		return fmt.Errorf("start reaper: %w", err)
	}
	_ = read.Close()
	// Collects the companion only if it exits while this process still runs.
	go func() { _ = cmd.Wait() }()

	reaper.mu.Lock()
	previous := reaper.pipe
	reaper.pipe = write
	reaper.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	return nil
}

func track(kind byte, payload string) func() {
	if !sendReaper('+', kind, payload) {
		return func() {}
	}
	var once sync.Once
	return func() { once.Do(func() { sendReaper('-', kind, payload) }) }
}

func sendReaper(op, kind byte, payload string) bool {
	reaper.mu.Lock()
	defer reaper.mu.Unlock()
	if reaper.pipe == nil {
		return false
	}
	_ = reaper.pipe.SetWriteDeadline(time.Now().Add(reaperWriteTimeout))
	if _, err := reaper.pipe.WriteString(string([]byte{op, kind}) + payload + "\n"); err != nil {
		slog.Warn("process reaper unavailable; engine children can outlive an abrupt exit", "err", err)
		_ = reaper.pipe.Close()
		reaper.pipe = nil
		return false
	}
	return true
}

// reaperDrainTimeout bounds the wait for killed processes to exit before the
// directories they may still be writing are removed.
const reaperDrainTimeout = 5 * time.Second

type reaperEntry struct {
	kind  byte
	id    int
	start int64
	path  string
	count int
}

// RunReaper is the companion's main loop. It records what the engine tracks and,
// once the engine is gone, kills each entry whose leader is still the process
// that was registered, so a pid reused after an exit is never signalled, and
// then removes each directory still tracked.
func RunReaper(in io.Reader) int {
	// Only the engine's exit ends reaping, so stray signals are ignored.
	signal.Ignore(syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
	reap(in)
	return 0
}

// reap blocks until in closes, then kills what is still tracked and removes
// tracked directories once the killed processes are gone.
func reap(in io.Reader) {
	entries := readReaperEntries(in)
	var killed []int
	var dirs []string
	for _, entry := range entries {
		if entry.kind == reaperDir {
			dirs = append(dirs, entry.path)
			continue
		}
		if start, alive := osprocess.StartTime(entry.id); !alive || start != entry.start {
			continue
		}
		if entry.kind == reaperSupervisor {
			if signalSupervisor(entry.id, entry.start, true) {
				killed = append(killed, entry.id)
			}
			continue
		}
		target := entry.id
		if entry.kind == reaperGroup {
			target = -entry.id
		}
		if syscall.Kill(target, syscall.SIGKILL) == nil {
			killed = append(killed, target)
		}
	}
	if len(dirs) == 0 {
		return
	}
	awaitSignalledGone(killed, reaperDrainTimeout)
	for _, dir := range dirs {
		_ = os.RemoveAll(dir)
	}
}

// readReaperEntries replays the engine's registrations until in closes and
// returns what is still tracked.
func readReaperEntries(in io.Reader) map[string]*reaperEntry {
	entries := map[string]*reaperEntry{}
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 3 {
			continue
		}
		key := line[1:]
		switch line[0] {
		case '+':
			entry := parseReaperEntry(line[1], line[2:])
			if entry == nil {
				continue
			}
			if prior := entries[key]; prior != nil && prior.start == entry.start {
				entry = prior
			}
			// A reused pid replaces the stale incarnation.
			entries[key] = entry
			entry.count++
		case '-':
			if entry := entries[key]; entry != nil {
				if entry.count--; entry.count <= 0 {
					delete(entries, key)
				}
			}
		}
	}
	return entries
}

// parseReaperEntry returns nil for a malformed registration or a process that
// already exited, since nothing of it is left to reap by that identity.
func parseReaperEntry(kind byte, payload string) *reaperEntry {
	switch kind {
	case reaperDir:
		if !reaperDirPath(payload) {
			return nil
		}
		return &reaperEntry{kind: kind, path: payload}
	case reaperGroup, reaperProcess, reaperSupervisor:
		id, err := strconv.Atoi(payload)
		if err != nil || id <= 0 {
			return nil
		}
		start, alive := osprocess.StartTime(id)
		if !alive {
			return nil
		}
		return &reaperEntry{kind: kind, id: id, start: start}
	}
	return nil
}

// awaitSignalledGone waits, within one shared bound, until no killed process
// or group member remains.
func awaitSignalledGone(targets []int, within time.Duration) {
	deadline := time.Now().Add(within)
	for _, target := range targets {
		for syscall.Kill(target, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
}
