package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

func logExternalLaunchProvenance(ctx context.Context, executionID, execPath string) {
	if execPath == "" {
		return
	}
	slog.InfoContext(ctx, "command external launch",
		"execution_id", executionID,
		"executable_path", execPath,
		"executable_sha256", externalExecutableSHA256(execPath),
	)
}

func externalExecutableSHA256(execPath string) string {
	target := execPath
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil && resolved != "" {
		target = resolved
	}
	f, err := fseffect.OpenRead(fseffect.Location{Root: filepath.Dir(target), Rel: filepath.Base(target)})
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
