package nativegen

import (
	"context"
	"fmt"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/inference"
)

// InspectDevices queries the packaged adapter without loading model weights.
func InspectDevices(ctx context.Context, binary, metallib string) (inference.Inventory, error) {
	args := []string{"--inventory"}
	if metallib != "" {
		args = append(args, "--metallib", metallib)
	}
	stdout, stderr, code, err := execpkg.RunSeparate(ctx, binary, args, execpkg.ExecOpts{
		Launch: execpkg.HostLaunch("generation_engine"), Timeout: 30 * time.Second,
		MaxOutputBytes: inference.MaxInventoryBytes,
	})
	if err != nil {
		return inference.Inventory{}, fmt.Errorf("native generation: inspect devices: %w", err)
	}
	if code != 0 {
		return inference.Inventory{}, fmt.Errorf("native generation: inventory exited with status %d: %s", code, stderr)
	}
	return inference.DecodeInventory(stdout)
}
