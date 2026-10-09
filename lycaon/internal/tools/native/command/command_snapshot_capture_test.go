package command_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Minimal valid 1x1 PNG bytes.
var testPNGBytes, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

func TestCommandSnapshotCaptureIncompatibleArgs(t *testing.T) {
	cmd, _ := newCommandTool(t)
	root := t.TempDir()
	tctx := commandToolContext(root, "sess-1", "worker-1")

	for _, field := range []string{"terminal_capture", "pipeline", "stdin", "background"} {
		args := map[string]any{
			"command":          "echo hello",
			"snapshot_capture": map[string]any{},
		}
		switch field {
		case "pipeline":
			delete(args, "command")
			args["pipeline"] = []any{"echo hello"}
		case "background":
			args[field] = true
		case "terminal_capture":
			args[field] = map[string]any{}
		default:
			args[field] = "some input"
		}

		_, err := cmd.Run(context.Background(), args, tctx)
		tr := tools.AsToolReject(err)
		if tr == nil || tr.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("field %s: expected TOOL_ARGS_INVALID reject, got %v", field, err)
		}
	}
}

func TestCommandDisallowedDesktopCapture(t *testing.T) {
	cmd, _ := newCommandTool(t)
	root := t.TempDir()
	tctx := commandToolContext(root, "sess-1", "worker-1")

	for _, disallowed := range []string{"screencapture -x out.png", "/usr/sbin/screencapture -x out.png", "scrot shot.png", "xwd -root"} {
		args := map[string]any{
			"command": disallowed,
		}
		_, err := cmd.Run(context.Background(), args, tctx)
		tr := tools.AsToolReject(err)
		if tr == nil || tr.Code != "DESKTOP_CAPTURE_DISALLOWED" {
			t.Fatalf("command %q: expected DESKTOP_CAPTURE_DISALLOWED, got %v", disallowed, err)
		}
	}
}

func TestCommandSnapshotCaptureMissingFileRejection(t *testing.T) {
	cmd, _ := newCommandTool(t)
	root := t.TempDir()
	tctx := commandToolContext(root, "sess-1", "worker-1")

	// Process exits 0 without writing to APP_SNAPSHOT.
	args := map[string]any{
		"command": "echo normal-output",
		"snapshot_capture": map[string]any{
			"caption": "test capture",
		},
	}
	_, err := cmd.Run(context.Background(), args, tctx)
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != "SNAPSHOT_FILE_NOT_PRODUCED" {
		t.Fatalf("expected SNAPSHOT_FILE_NOT_PRODUCED, got %v", err)
	}
}

func TestCommandSnapshotCaptureSuccess(t *testing.T) {
	cmd, _ := newCommandTool(t)
	root := t.TempDir()
	tctx := commandToolContext(root, "sess-1", "worker-1")
	tctx.Out = &tools.ToolInvocationOut{}

	helperScript := filepath.Join(root, "snapshot_app.sh")
	pngFile := filepath.Join(root, "sample.png")
	if err := os.WriteFile(pngFile, testPNGBytes, 0644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	scriptContent := "#!/bin/sh\ncp \"" + pngFile + "\" \"$APP_SNAPSHOT\"\nexit 0\n"
	if err := os.WriteFile(helperScript, []byte(scriptContent), 0755); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	args := map[string]any{
		"command": "/bin/sh " + helperScript,
		"snapshot_capture": map[string]any{
			"caption": "Chess board preview",
			"scale":   2.0,
		},
	}

	outStr, err := cmd.Run(context.Background(), args, tctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tctx.Out.Visual == nil {
		t.Fatal("expected tctx.Out.Visual to be populated")
	}
	if !tctx.Out.Visual.Perceive {
		t.Fatal("expected Visual.Perceive to be true")
	}
	if tctx.Out.Visual.Source != api.VisualArtifactSourceCapture {
		t.Fatalf("expected source %q, got %q", api.VisualArtifactSourceCapture, tctx.Out.Visual.Source)
	}
	if tctx.Out.Visual.Caption != "Chess board preview" {
		t.Fatalf("caption = %q", tctx.Out.Visual.Caption)
	}
	if tctx.Out.Visual.Width != 1 || tctx.Out.Visual.Height != 1 {
		t.Fatalf("dimensions = %dx%d, want 1x1", tctx.Out.Visual.Width, tctx.Out.Visual.Height)
	}

	var parsed struct {
		ExitCode        int `json:"exit_code"`
		SnapshotCapture struct {
			Surface   string `json:"surface"`
			Format    string `json:"format"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			SizeBytes int64  `json:"size_bytes"`
			Caption   string `json:"caption"`
		} `json:"snapshot_capture"`
	}
	if err := json.Unmarshal([]byte(outStr), &parsed); err != nil {
		t.Fatalf("failed to parse output json: %v, raw: %s", err, outStr)
	}

	if parsed.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", parsed.ExitCode)
	}
	if parsed.SnapshotCapture.Surface != "gui" {
		t.Fatalf("surface = %q, want gui", parsed.SnapshotCapture.Surface)
	}
	if !strings.EqualFold(parsed.SnapshotCapture.Format, "png") {
		t.Fatalf("format = %q, want png", parsed.SnapshotCapture.Format)
	}
	if parsed.SnapshotCapture.Width != 1 || parsed.SnapshotCapture.Height != 1 {
		t.Fatalf("dimensions in json = %dx%d, want 1x1", parsed.SnapshotCapture.Width, parsed.SnapshotCapture.Height)
	}
}
