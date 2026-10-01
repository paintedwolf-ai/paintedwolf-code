package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/terminal"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

func TestTerminalSendReadTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	dir := t.TempDir()
	script := filepath.Join(dir, "drive.sh")
	body := "#!/bin/sh\nprintf 'PROMPT> '\nIFS= read -r line\nprintf 'GOT:%s\\n' \"$line\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		testutil.FailErr(t, "write script", err)
	}

	tctx := tools.ToolContext{
		SessionID: "sess", ProjectID: "proj", Out: &tools.ToolInvocationOut{},
		Roots: []projectroot.RootRef{{ID: "main", Path: dir, IsPrimary: true}},
	}
	openRaw, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": script,
	}, tctx)
	testutil.FailErr(t, "terminal_open", err)
	var opened terminal.OpenResult
	testutil.FailErr(t, "unmarshal open", json.Unmarshal([]byte(openRaw), &opened))
	if opened.Observe != "screen" || strings.Contains(openRaw, `"log"`) {
		t.Fatalf("open must be one screen observation without duplicate log: %s", openRaw)
	}
	if _, ok := surveyreceipt.Parse(openRaw); !ok {
		t.Fatalf("open screen must carry receipt: %s", openRaw)
	}
	handle := opened.ID
	defer func() {
		_, _ = reg.Run(context.Background(), terminal.CloseToolName, map[string]any{"id": handle}, tctx)
	}()

	promptOut, err := reg.Run(context.Background(), terminal.ReadToolName, map[string]any{
		"id": handle, "idle_ms": 150.0, "timeout_ms": 2000.0,
	}, tctx)
	testutil.FailErr(t, "terminal_read prompt", err)
	if tctx.Out.DisplaySubject != script {
		t.Fatalf("terminal target = %q, want %q", tctx.Out.DisplaySubject, script)
	}
	if !strings.Contains(promptOut, "PROMPT>") {
		t.Fatalf("prompt = %s", promptOut)
	}

	_, err = reg.Run(context.Background(), terminal.SendToolName, map[string]any{
		"id": handle, "input": "ok{Enter}",
	}, tctx)
	testutil.FailErr(t, "terminal_send", err)

	answerOut, err := reg.Run(context.Background(), terminal.ReadToolName, map[string]any{
		"id": handle, "idle_ms": 150.0, "timeout_ms": 2000.0,
	}, tctx)
	testutil.FailErr(t, "terminal_read answer", err)
	if !strings.Contains(answerOut, "GOT:ok") {
		t.Fatalf("answer = %s", answerOut)
	}
}

func TestTerminalOpenRejectsMetachar(t *testing.T) {
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))
	dir := t.TempDir()
	tctx := tools.ToolContext{
		SessionID: "s", ProjectID: "p",
		Roots: []projectroot.RootRef{{ID: "main", Path: dir, IsPrimary: true}},
	}
	// Substitution never expands, so it stays a metacharacter rejection here.
	_, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": "echo $(whoami)",
	}, tctx)
	if err == nil {
		t.Fatal("expected metachar reject")
	}
	if !errors.Is(err, argv.ErrShellMetacharacters) && !strings.Contains(err.Error(), "metachar") {
		t.Fatalf("err = %v, want shell metachar", err)
	}

	// A sequence parses, but a pty holds one process — so it is refused as
	// composition rather than as malformed argv.
	_, err = reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": "echo hi; rm -rf /tmp/x",
	}, tctx)
	if !errors.Is(err, commandsurface.ErrSequenceUnsupported) {
		t.Fatalf("err = %v, want ErrSequenceUnsupported", err)
	}
}

func TestTerminalOpenRejectsOversizedWinSize(t *testing.T) {
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))
	dir := t.TempDir()
	tctx := tools.ToolContext{
		SessionID: "s", ProjectID: "p",
		Roots: []projectroot.RootRef{{ID: "main", Path: dir, IsPrimary: true}},
	}
	_, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": "echo hi", "winsize": map[string]any{"cols": 241.0, "rows": 24.0},
	}, tctx)
	reject := tools.AsToolReject(err)
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" || reject.Data["reason"] != "winsize_out_of_range" {
		t.Fatalf("oversized winsize reject = %#v (err = %v)", reject, err)
	}
}

func TestTerminalOpenSendDefaultSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	dir := t.TempDir()
	script := filepath.Join(dir, "menu.sh")
	body := "#!/bin/sh\nprintf 'MENU\\n'\nIFS= read -r line\nprintf 'ECHO:%s\\n' \"$line\"\nIFS= read -r _\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		testutil.FailErr(t, "write script", err)
	}
	out := &tools.ToolInvocationOut{}
	tctx := tools.ToolContext{
		SessionID: "sess-snap", ProjectID: "proj",
		Roots: []projectroot.RootRef{{ID: "main", Path: dir, IsPrimary: true}},
		Out:   out,
	}
	openRaw, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": script,
	}, tctx)
	testutil.FailErr(t, "terminal_open", err)
	var opened terminal.OpenResult
	testutil.FailErr(t, "unmarshal open", json.Unmarshal([]byte(openRaw), &opened))
	if opened.Surface != "tui" || opened.Snapshot == nil {
		t.Fatalf("open missing default snapshot: %+v", opened)
	}
	found := false
	for _, line := range opened.Snapshot.Lines {
		if strings.Contains(line, "MENU") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("open grid missing MENU: %v", opened.Snapshot.Lines)
	}
	handle := opened.ID
	defer func() {
		_, _ = reg.Run(context.Background(), terminal.CloseToolName, map[string]any{"id": handle}, tctx)
	}()

	sendRaw, err := reg.Run(context.Background(), terminal.SendToolName, map[string]any{
		"id": handle, "input": "hi{Enter}",
	}, tctx)
	testutil.FailErr(t, "terminal_send", err)
	var sent terminal.SendResult
	testutil.FailErr(t, "unmarshal send", json.Unmarshal([]byte(sendRaw), &sent))
	if sent.Observe != "screen" || strings.Contains(sendRaw, `"log"`) {
		t.Fatalf("send must be one screen observation without duplicate log: %s", sendRaw)
	}
	if sent.Surface != "tui" || sent.Snapshot == nil {
		t.Fatalf("send missing default snapshot: %+v", sent)
	}
	found = false
	for _, line := range sent.Snapshot.Lines {
		if strings.Contains(line, "ECHO:hi") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("send grid missing ECHO:hi: %v", sent.Snapshot.Lines)
	}
}

func TestTerminalObserveAckDoesNotCaptureScreen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))
	dir := t.TempDir()
	script := filepath.Join(dir, "held.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'READY\\n'\nIFS= read -r _\n"), 0o755); err != nil {
		testutil.FailErr(t, "write script", err)
	}
	tctx := tools.ToolContext{SessionID: "ack", ProjectID: "proj", Roots: []projectroot.RootRef{{ID: "main", Path: dir, IsPrimary: true}}}
	raw, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{"command": script, "observe": "ack"}, tctx)
	testutil.FailErr(t, "terminal_open ack", err)
	var opened terminal.OpenResult
	testutil.FailErr(t, "unmarshal open", json.Unmarshal([]byte(raw), &opened))
	if opened.Observe != "ack" || opened.Snapshot != nil || opened.Delta != nil {
		t.Fatalf("ack must not include a screen or delta: %+v", opened)
	}
	_, _ = reg.Run(context.Background(), terminal.CloseToolName, map[string]any{"id": opened.ID}, tctx)
}
