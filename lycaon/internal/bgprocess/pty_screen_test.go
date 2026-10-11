package bgprocess

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func configurePTYCaptureProjection(reg *Registry) {
	reg.Output.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
}

func TestPTYScreenAltScreenSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	reg := newTestRegistry(t, DefaultConfig(), Hooks{})
	configurePTYCaptureProjection(reg)
	dir := t.TempDir()
	runner := hostcmd.NewRunner()
	scriptPath := filepath.Join(dir, "alt.sh")
	// Enter alt screen, clear, paint two fixed rows, then block on input.
	body := "#!/bin/sh\n" +
		"printf '\\033[?1049h\\033[H\\033[2J'\n" +
		"printf 'TITLE-ROW\\n'\n" +
		"printf 'BODY-ROW\\n'\n" +
		"IFS= read -r _\n"
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		testutil.FailErr(t, "write alt.sh", err)
	}
	handle, err := reg.Terminal.StartPTY(context.Background(), "s1", "", "p1", hostcmd.Request{Launch: lycexec.HostLaunch("bgprocess pty test"),
		ProjectDir: dir,
		Stages:     []lycexec.Stage{{Name: scriptPath}},
	}, runner, lycexec.WinSize{Cols: lycexec.DefaultPTYCols, Rows: lycexec.DefaultPTYRows}, confine.SpawnFacts{})
	testutil.FailErr(t, "StartPTY", err)
	defer func() { _, _ = reg.Terminal.ClosePTY("s1", handle) }()

	// Wait for paint via idle read, but do not rely on that for the grid —
	// SnapshotPTY must work even after ReadPTY advances its cursor.
	_, err = reg.Terminal.ReadPTY("s1", handle, PTYReadOpts{Idle: 150 * time.Millisecond, Timeout: 2 * time.Second})
	testutil.FailErr(t, "ReadPTY settle", err)

	res1, err := reg.Terminal.SnapshotPTY(context.Background(), "s1", handle, PTYReadOpts{Idle: 150 * time.Millisecond, Timeout: 2 * time.Second})
	testutil.FailErr(t, "SnapshotPTY 1", err)
	snap1 := res1.Screen
	if snap1.Cols != int(lycexec.DefaultPTYCols) || snap1.Rows != int(lycexec.DefaultPTYRows) {
		t.Fatalf("winsize = %dx%d, want %dx%d", snap1.Cols, snap1.Rows, lycexec.DefaultPTYCols, lycexec.DefaultPTYRows)
	}
	if !snap1.AltScreen {
		t.Fatal("expected alt_screen true")
	}
	if len(snap1.Lines) != snap1.Rows {
		t.Fatalf("lines = %d, want rows=%d", len(snap1.Lines), snap1.Rows)
	}
	if !strings.HasPrefix(strings.TrimRight(snap1.Lines[0], " "), "TITLE-ROW") {
		t.Fatalf("line0 = %q", snap1.Lines[0])
	}
	if !strings.HasPrefix(strings.TrimRight(snap1.Lines[1], " "), "BODY-ROW") {
		t.Fatalf("line1 = %q", snap1.Lines[1])
	}
	for i, line := range snap1.Lines {
		if len([]rune(line)) != snap1.Cols {
			t.Fatalf("line %d width = %d, want %d (%q)", i, len([]rune(line)), snap1.Cols, line)
		}
	}

	res2, err := reg.Terminal.SnapshotPTY(context.Background(), "s1", handle, PTYReadOpts{Idle: 50 * time.Millisecond, Timeout: 2 * time.Second})
	testutil.FailErr(t, "SnapshotPTY 2", err)
	b1, err := json.Marshal(res1.Screen)
	testutil.FailErr(t, "marshal snap1", err)
	b2, err := json.Marshal(res2.Screen)
	testutil.FailErr(t, "marshal snap2", err)
	if string(b1) != string(b2) {
		t.Fatalf("non-deterministic grids:\n%s\n%s", b1, b2)
	}

	// Snapshot must not disturb subsequent incremental reads — send Enter and
	// expect the read path still returns something without hanging.
	testutil.FailErr(t, "WritePTY", reg.Terminal.WritePTY("s1", handle, []byte("\r")))
	readAfter, err := reg.Terminal.ReadPTY("s1", handle, PTYReadOpts{Idle: 150 * time.Millisecond, Timeout: 2 * time.Second})
	testutil.FailErr(t, "ReadPTY after snapshot", err)
	_ = readAfter
}

func TestPTYScreenUnitWrite(t *testing.T) {
	s := newPTYScreen(10, 3)
	defer s.Close()
	// Absolute cursor + clear so each painted row starts at column 0.
	s.Write([]byte("\x1b[?1049h\x1b[H\x1b[2J\x1b[1;1HHELLO\x1b[2;1HWORLD"))
	snap := s.snapshot()
	if !snap.AltScreen {
		t.Fatal("want alt screen")
	}
	if !strings.HasPrefix(strings.TrimRight(snap.Lines[0], " "), "HELLO") {
		t.Fatalf("line0 = %q", snap.Lines[0])
	}
	if !strings.HasPrefix(strings.TrimRight(snap.Lines[1], " "), "WORLD") {
		t.Fatalf("line1 = %q", snap.Lines[1])
	}
	again := s.snapshot()
	b1, _ := json.Marshal(snap)
	b2, _ := json.Marshal(again)
	if string(b1) != string(b2) {
		t.Fatalf("unit screen non-deterministic")
	}
}

// TestPTYScreenDSRDoesNotBlockWrite checks reply-pipe draining.
func TestPTYScreenDSRDoesNotBlockWrite(t *testing.T) {
	s := newPTYScreen(80, 24)
	defer s.Close()

	var got sync.Mutex
	var replies []byte
	s.setOnReply(func(b []byte) {
		got.Lock()
		replies = append(replies, b...)
		got.Unlock()
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		// One DSR is enough to deadlock an undrained pipe; spam to be sure.
		for i := 0; i < 64; i++ {
			s.Write([]byte("\x1b[6n"))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ptyScreen.Write blocked on DSR replies — reply drain missing")
	}

	// Snapshot must also complete (it takes RLock that Write held across the pipe).
	snapDone := make(chan ScreenSnapshot, 1)
	go func() { snapDone <- s.snapshot() }()
	select {
	case snap := <-snapDone:
		if snap.Cols != 80 || snap.Rows != 24 {
			t.Fatalf("snapshot winsize %dx%d", snap.Cols, snap.Rows)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot blocked after DSR writes")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got.Lock()
		n := len(replies)
		got.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected CPR replies forwarded via onReply")
}

// TestPTYScreenWideCellsKeepRuneIndexOnItsColumn checks grid alignment.
func TestPTYScreenWideCellsKeepRuneIndexOnItsColumn(t *testing.T) {
	s := newPTYScreen(10, 2)
	defer s.Close()
	s.Write([]byte("\x1b[H\x1b[2J\x1b[1;1H世x"))
	snap := s.snapshot()
	runes := []rune(snap.Lines[0])
	if len(runes) != snap.Cols {
		t.Fatalf("row width = %d runes, want %d columns", len(runes), snap.Cols)
	}
	if runes[0] != '世' {
		t.Fatalf("column 0 = %q, want the wide cell", string(runes[0]))
	}
	if runes[2] != 'x' {
		t.Fatalf("column 2 = %q, want the cell after a two-column glyph", string(runes[2]))
	}
}
