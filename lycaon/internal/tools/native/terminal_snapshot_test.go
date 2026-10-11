package native

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/terminal"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTerminalSnapshotAltScreenGrid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	dir := t.TempDir()
	script := filepath.Join(dir, "alt.sh")
	body := "#!/bin/sh\n" +
		"printf '\\033[?1049h\\033[H\\033[2J'\n" +
		"printf 'TITLE-ROW\\n'\n" +
		"printf 'BODY-ROW\\n'\n" +
		"IFS= read -r _\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		testutil.FailErr(t, "write script", err)
	}
	out := &tools.ToolInvocationOut{}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess",
			ProjectID: "proj"},
		Source:  tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "main", Path: dir, IsPrimary: true}}},
		Effects: tools.InvocationEffects{Out: out},
	}
	openRaw, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": script,
		"winsize": map[string]any{"cols": 80.0, "rows": 24.0},
	}, tctx)
	testutil.FailErr(t, "terminal_open", err)
	var opened terminal.OpenResult
	testutil.FailErr(t, "unmarshal open", json.Unmarshal([]byte(openRaw), &opened))
	handle := opened.ID
	defer func() {
		_, _ = reg.Run(context.Background(), terminal.CloseToolName, map[string]any{"id": handle}, tctx)
	}()

	// Advance the read cursor first — snapshot must still return the full grid.
	_, err = reg.Run(context.Background(), terminal.ReadToolName, map[string]any{
		"id": handle, "idle_ms": 150.0, "timeout_ms": 2000.0,
	}, tctx)
	testutil.FailErr(t, "terminal_read settle", err)

	raw1, err := reg.Run(context.Background(), terminal.SnapshotToolName, map[string]any{
		"id": handle, "idle_ms": 150.0, "timeout_ms": 2000.0, "caption": "TUI",
	}, tctx)
	testutil.FailErr(t, "terminal_snapshot 1", err)
	var snap1 terminal.SnapshotResult
	testutil.FailErr(t, "unmarshal snap1", json.Unmarshal([]byte(raw1), &snap1))
	if snap1.Surface != "tui" {
		t.Fatalf("surface = %q want tui", snap1.Surface)
	}
	if !snap1.State.AltScreen {
		t.Fatal("expected alt_screen")
	}
	if snap1.Snapshot.Cols != 80 || snap1.Snapshot.Rows != 24 {
		t.Fatalf("grid winsize = %dx%d", snap1.Snapshot.Cols, snap1.Snapshot.Rows)
	}
	if len(snap1.Snapshot.Lines) != 24 {
		t.Fatalf("lines = %d", len(snap1.Snapshot.Lines))
	}
	if !strings.HasPrefix(strings.TrimRight(snap1.Snapshot.Lines[0], " "), "TITLE-ROW") {
		t.Fatalf("line0 = %q", snap1.Snapshot.Lines[0])
	}
	if !strings.HasPrefix(strings.TrimRight(snap1.Snapshot.Lines[1], " "), "BODY-ROW") {
		t.Fatalf("line1 = %q", snap1.Snapshot.Lines[1])
	}
	if snap1.Caption != "TUI" {
		t.Fatalf("caption = %q", snap1.Caption)
	}
	if out.Visual == nil || out.Visual.Source != api.VisualArtifactSourceCapture {
		t.Fatalf("expected capture visual artifact, got %#v", out.Visual)
	}
	if len(out.Visual.Bytes) == 0 || !strings.HasPrefix(out.Visual.Mime, "image/") {
		t.Fatalf("visual mime/bytes = %q/%d", out.Visual.Mime, len(out.Visual.Bytes))
	}
	if snap1.Mime == "" || snap1.Width <= 0 || snap1.Height <= 0 {
		t.Fatalf("content artifact fields mime=%q %dx%d", snap1.Mime, snap1.Width, snap1.Height)
	}

	raw2, err := reg.Run(context.Background(), terminal.SnapshotToolName, map[string]any{
		"id": handle, "idle_ms": 50.0, "timeout_ms": 2000.0,
	}, tctx)
	testutil.FailErr(t, "terminal_snapshot 2", err)
	var snap2 terminal.SnapshotResult
	testutil.FailErr(t, "unmarshal snap2", json.Unmarshal([]byte(raw2), &snap2))
	b1, _ := json.Marshal(snap1.Snapshot)
	b2, _ := json.Marshal(snap2.Snapshot)
	if string(b1) != string(b2) {
		t.Fatalf("non-deterministic snapshot grids:\n%s\n%s", b1, b2)
	}

	// Coexist with read — send Enter and still get a non-hanging next read.
	_, err = reg.Run(context.Background(), terminal.SendToolName, map[string]any{
		"id": handle, "input": "{Enter}",
	}, tctx)
	testutil.FailErr(t, "terminal_send", err)
	_, err = reg.Run(context.Background(), terminal.ReadToolName, map[string]any{
		"id": handle, "idle_ms": 100.0, "timeout_ms": 2000.0,
	}, tctx)
	testutil.FailErr(t, "terminal_read after snapshot", err)
}
