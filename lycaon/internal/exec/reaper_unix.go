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

func track(kind byte, id int) func() {
	if id <= 0 || !sendReaper('+', kind, id) {
		return func() {}
	}
	var once sync.Once
	return func() { once.Do(func() { sendReaper('-', kind, id) }) }
}

func sendReaper(op, kind byte, id int) bool {
	reaper.mu.Lock()
	defer reaper.mu.Unlock()
	if reaper.pipe == nil {
		return false
	}
	_ = reaper.pipe.SetWriteDeadline(time.Now().Add(reaperWriteTimeout))
	if _, err := reaper.pipe.WriteString(string([]byte{op, kind}) + strconv.Itoa(id) + "\n"); err != nil {
		slog.Warn("process reaper unavailable; engine children can outlive an abrupt exit", "err", err)
		_ = reaper.pipe.Close()
		reaper.pipe = nil
		return false
	}
	return true
}

type reaperEntry struct {
	kind  byte
	id    int
	start int64
	count int
}

// RunReaper is the companion's main loop. It records what the engine tracks and,
// once the engine is gone, kills each entry whose leader is still the process
// that was registered, so a pid reused after an exit is never signalled.
func RunReaper(in io.Reader) int {
	// Only the engine's exit ends reaping, so stray signals are ignored.
	signal.Ignore(syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
	reap(in)
	return 0
}

// reap blocks until in closes, then kills what is still tracked.
func reap(in io.Reader) {
	entries := map[string]*reaperEntry{}
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 3 || (line[1] != reaperGroup && line[1] != reaperProcess) {
			continue
		}
		id, err := strconv.Atoi(line[2:])
		if err != nil || id <= 0 {
			continue
		}
		key := line[1:]
		switch line[0] {
		case '+':
			start, alive := osprocess.StartTime(id)
			if !alive {
				// Already exited; nothing of it is left to reap by this identity.
				continue
			}
			entry := entries[key]
			if entry == nil || entry.start != start {
				// A reused id replaces the stale incarnation.
				entry = &reaperEntry{kind: line[1], id: id, start: start}
				entries[key] = entry
			}
			entry.count++
		case '-':
			if entry := entries[key]; entry != nil {
				if entry.count--; entry.count <= 0 {
					delete(entries, key)
				}
			}
		}
	}
	for _, entry := range entries {
		if start, alive := osprocess.StartTime(entry.id); !alive || start != entry.start {
			continue
		}
		target := entry.id
		if entry.kind == reaperGroup {
			target = -entry.id
		}
		_ = syscall.Kill(target, syscall.SIGKILL)
	}
}
