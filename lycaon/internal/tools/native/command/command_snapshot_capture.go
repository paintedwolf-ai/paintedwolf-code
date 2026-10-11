package command

import (
	"bytes"
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

// SnapshotCaptureResult describes a captured native GUI application screenshot.
type SnapshotCaptureResult struct {
	Surface   string `json:"surface"`
	Format    string `json:"format"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SizeBytes int64  `json:"size_bytes"`
	Caption   string `json:"caption,omitempty"`
}

func runCommandSnapshotCapture(
	ctx context.Context,
	registry *bgprocess.Registry,
	tracker *CommandFailureTracker,
	runner *hostcmd.Runner,
	boundary *sandbox.Boundary,
	args map[string]any,
	tctx tools.ToolContext,
	extraWriteRoots []string,
	toolName string,
) (RunOutcome, error) {
	if toolName != "command" {
		return RunOutcome{}, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "snapshot_capture_is_command_only"})
	}
	for _, key := range []string{"terminal_capture", "pipeline", "stdin", "stdin_from", "stdout_to", "stderr_to", "background"} {
		if value, exists := args[key]; exists && value != nil && value != false && value != "" {
			return RunOutcome{}, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "snapshot_capture_incompatible", "field": key})
		}
	}

	cfg, _ := args["snapshot_capture"].(map[string]any)
	envVar := "APP_SNAPSHOT"
	if v, ok := cfg["env_var"].(string); ok && strings.TrimSpace(v) != "" {
		envVar = strings.TrimSpace(v)
	}
	scale := 2.0
	if v, ok := cfg["scale"].(float64); ok && v > 0 {
		scale = v
	}
	view, _ := cfg["view"].(string)
	caption, _ := cfg["caption"].(string)
	caption = strings.TrimSpace(caption)
	if caption == "" {
		caption = "Native app snapshot"
	}

	scratchDir := strings.TrimSpace(tctx.Host.SessionScratchDir)
	if scratchDir == "" {
		scratchDir = os.TempDir()
	}
	outFileName := fmt.Sprintf("snapshot-%s.png", uuid.NewString())
	outPath := filepath.Join(scratchDir, outFileName)
	extraWriteRoots = append(extraWriteRoots, filepath.Dir(outPath))

	childEnv := make(map[string]any)
	if existingEnv, ok := args["env"].(map[string]any); ok {
		for k, v := range existingEnv {
			childEnv[k] = v
		}
	}
	childEnv[envVar] = outPath
	childEnv["APP_SNAPSHOT_SCALE"] = fmt.Sprintf("%.1f", scale)
	if strings.TrimSpace(view) != "" {
		childEnv["APP_SNAPSHOT_VIEW"] = strings.TrimSpace(view)
	}

	argsCopy := make(map[string]any, len(args))
	for k, v := range args {
		argsCopy[k] = v
	}
	argsCopy["env"] = childEnv
	delete(argsCopy, "snapshot_capture")

	outcome, err := runCommandForeground(
		ctx, registry, tracker, runner, boundary, argsCopy, tctx,
		commandWaitBudget(argsCopy), true, extraWriteRoots, toolName,
	)
	if err != nil {
		return RunOutcome{}, err
	}
	if !outcome.Finished {
		return outcome, nil
	}
	if outcome.Snapshot.ExitCode != 0 || outcome.Snapshot.TerminationReason != bgprocess.TerminationExited {
		return outcome, nil
	}

	f, err := fseffect.OpenRead(fseffect.Location{Root: filepath.Dir(outPath), Rel: filepath.Base(outPath)})
	if err != nil {
		return RunOutcome{}, toolrejection.RejectInvalidArguments("SNAPSHOT_FILE_NOT_PRODUCED", map[string]any{
			"path":    outPath,
			"env_var": envVar,
			"reason":  "snapshot_file_missing_on_zero_exit",
		})
	}
	defer func() { _ = f.Close() }()

	rawBytes, err := io.ReadAll(io.LimitReader(f, visual.MaxRasterBytes().Int64()))
	if err != nil || len(rawBytes) == 0 {
		return RunOutcome{}, toolrejection.RejectInvalidArguments("SNAPSHOT_FILE_NOT_PRODUCED", map[string]any{
			"path":    outPath,
			"env_var": envVar,
			"reason":  "snapshot_file_missing_on_zero_exit",
		})
	}

	cfgImg, format, err := image.DecodeConfig(bytes.NewReader(rawBytes))
	if err != nil {
		format = "png"
	}

	mime := "image/" + format
	if format == "jpg" {
		mime = "image/jpeg"
	}
	norm, err := providerwire.NormalizeImageBytes(rawBytes, mime, visual.MaxRasterBytes())
	if err != nil {
		return RunOutcome{}, &toolrejection.ToolReject{
			Code: "IMAGE_CORRUPTED",
			Data: map[string]any{
				"path":             outPath,
				"format":           format,
				"rejection_reason": err.Error(),
			},
		}
	}

	if tctx.Effects.Out == nil {
		tctx.Effects.Out = &tools.ToolInvocationOut{}
	}
	tctx.Effects.Out.Visual = &tools.VisualCapture{
		Mime:      norm.Mime,
		Bytes:     norm.Bytes,
		Source:    api.VisualArtifactSourceCapture,
		Caption:   caption,
		Width:     cfgImg.Width,
		Height:    cfgImg.Height,
		Perceive:  true,
		Projected: true,
	}

	outcome.SnapshotCapture = &SnapshotCaptureResult{
		Surface:   "gui",
		Format:    format,
		Width:     cfgImg.Width,
		Height:    cfgImg.Height,
		SizeBytes: int64(len(rawBytes)),
		Caption:   caption,
	}

	return outcome, nil
}
