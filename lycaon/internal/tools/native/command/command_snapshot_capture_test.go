package command_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
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
		tr := toolrejection.AsToolReject(err)
		if tr == nil || tr.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("field %s: expected TOOL_ARGS_INVALID reject, got %v", field, err)
		}
	}
}

func TestCommandSnapshotCaptureMissingFileRejection(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load snapshot feedback", err)
	formatter := guidance.NewStaticRejectFormatter(hints)
	for _, envVar := range []string{"APP_SNAPSHOT", "PW_CAPTURE_FILE"} {
		t.Run(envVar, func(t *testing.T) {
			cmd, _ := newCommandTool(t)
			tctx := commandToolContext(t.TempDir(), "sess-1", "worker-1")
			_, err := cmd.Run(t.Context(), map[string]any{
				"command":          "echo normal-output",
				"snapshot_capture": map[string]any{"env_var": envVar},
			}, tctx)
			rejected := toolrejection.AsToolReject(err)
			if rejected == nil || rejected.Code != "SNAPSHOT_FILE_NOT_PRODUCED" || rejected.Data["env_var"] != envVar {
				t.Fatalf("snapshot refusal lost its configured input: %v", err)
			}
			text, err := formatter.Format(rejected.Code, rejected.Data)
			testutil.FailErr(t, "render snapshot observation", err)
			if !strings.Contains(text, envVar) || strings.Contains(text, "Fix:") || strings.Contains(text, "Next:") || (envVar != "APP_SNAPSHOT" && strings.Contains(text, "APP_SNAPSHOT")) {
				t.Fatalf("snapshot feedback guessed a contract or repair: %s", text)
			}
		})
	}
}

func TestCommandSnapshotCaptureSuccess(t *testing.T) {
	cmd, _ := newCommandTool(t)
	root := t.TempDir()
	tctx := commandToolContext(root, "sess-1", "worker-1")
	tctx.Effects.Out = &tools.ToolInvocationOut{}

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

	if tctx.Effects.Out.Visual == nil {
		t.Fatal("expected tctx.Effects.Out.Visual to be populated")
	}
	if !tctx.Effects.Out.Visual.Perceive {
		t.Fatal("expected Visual.Perceive to be true")
	}
	if tctx.Effects.Out.Visual.Source != api.VisualArtifactSourceCapture {
		t.Fatalf("expected source %q, got %q", api.VisualArtifactSourceCapture, tctx.Effects.Out.Visual.Source)
	}
	if tctx.Effects.Out.Visual.Caption != "Chess board preview" {
		t.Fatalf("caption = %q", tctx.Effects.Out.Visual.Caption)
	}
	if tctx.Effects.Out.Visual.Width != 1 || tctx.Effects.Out.Visual.Height != 1 {
		t.Fatalf("dimensions = %dx%d, want 1x1", tctx.Effects.Out.Visual.Width, tctx.Effects.Out.Visual.Height)
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
