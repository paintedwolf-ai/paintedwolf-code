package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fseffect"
)

const defaultExternalLaunchDigestMaxBytes int64 = 64 << 20

// logExternalLaunchProvenance emits opt-in executable provenance on the actual
// started edge using the path captured by confined preparation. When the
// development harness evidence channel is inactive, it does no file I/O.
func logExternalLaunchProvenance(ctx context.Context, executionID, execPath string) {
	if !configdir.IsHarnessChannel() {
		return
	}
	digest, status := boundedExecutableSHA256(ctx, execPath, defaultExternalLaunchDigestMaxBytes)
	slog.InfoContext(ctx, "command external launch",
		"execution_id", executionID,
		"executable_path", execPath,
		"executable_sha256", digest,
		"executable_sha256_status", status,
		"executable_provenance_basis", "prepared_file",
	)
}

func boundedExecutableSHA256(ctx context.Context, execPath string, maxBytes int64) (string, string) {
	if err := ctx.Err(); err != nil {
		return "", "canceled"
	}
	if execPath == "" {
		return "", "unavailable"
	}
	target := execPath
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil && resolved != "" {
		target = resolved
	}
	f, err := fseffect.OpenRead(fseffect.Location{Root: filepath.Dir(target), Rel: filepath.Base(target)})
	if err != nil {
		return "", "unavailable"
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	buf := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", "canceled"
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			total += int64(n)
			if maxBytes > 0 && total > maxBytes {
				return "", "over_budget"
			}
			_, _ = h.Write(buf[:n])
		}
		if readErr == io.EOF {
			return hex.EncodeToString(h.Sum(nil)), "ok"
		}
		if readErr != nil {
			return "", "unavailable"
		}
	}
}
