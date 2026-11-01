package nativegen

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/inference/driverenv"
)

// InspectDevices queries the packaged adapter without loading model weights.
func InspectDevices(ctx context.Context, binary, metallib string) (inference.Inventory, error) {
	environment, cleanup, err := driverenv.Prepare(configlayout.EngineRoot())
	if err != nil {
		return inference.Inventory{}, fmt.Errorf("native generation: prepare drivers: %w", err)
	}
	defer cleanup()
	args := []string{"--inventory"}
	if metallib != "" {
		args = append(args, "--metallib", metallib)
	}
	stdout, stderr, code, err := execpkg.RunSeparate(ctx, binary, args, execpkg.ExecOpts{
		Launch: execpkg.HostLaunch("generation_engine"), Timeout: 30 * time.Second,
		MaxOutputBytes: inference.MaxInventoryBytes,
		AppendEnv:      environment,
	})
	if err != nil {
		return inference.Inventory{}, fmt.Errorf("native generation: inspect devices: %w", err)
	}
	if code != 0 {
		return inference.Inventory{}, fmt.Errorf("native generation: inventory exited with status %d: %s", code, stderr)
	}
	return inference.DecodeInventory(stdout)
}
