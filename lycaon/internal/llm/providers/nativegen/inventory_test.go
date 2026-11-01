package nativegen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/testutil"
)

func inventoryFixture() {
	if !slices.Equal(os.Args[1:], []string{"--inventory", "--metallib", "/fixture/mlx.metallib"}) {
		os.Exit(9)
	}
	switch os.Getenv("PW_NATIVEGEN_INVENTORY_CASE") {
	case "failure":
		fmt.Fprint(os.Stderr, "fixture device failure")
		os.Exit(7)
	case "oversize":
		fmt.Fprint(os.Stdout, strings.Repeat("x", inference.MaxInventoryBytes+1))
	case "invalid":
		fmt.Fprint(os.Stdout, `{}`)
	default:
		fmt.Fprint(os.Stderr, "fixture driver diagnostics")
		fmt.Fprint(os.Stdout, `{"protocol":1,"backend":"mlx","devices":[]}`)
	}
}

func TestInspectDevicesBoundsProcessOutput(t *testing.T) {
	t.Setenv("PW_NATIVEGEN_PROCESS_FIXTURE", "3")
	binary, err := os.Executable()
	testutil.FailErr(t, "inventory fixture executable", err)
	for _, scenario := range []string{"valid", "failure", "oversize", "invalid"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("PW_NATIVEGEN_INVENTORY_CASE", scenario)
			got, err := InspectDevices(t.Context(), binary, "/fixture/mlx.metallib")
			switch scenario {
			case "valid":
				testutil.FailErr(t, "inspect fixture devices", err)
				if got.Backend != "mlx" || got.Devices == nil {
					t.Fatalf("inventory = %+v", got)
				}
			case "failure":
				if err == nil || !strings.Contains(err.Error(), "status 7: fixture device failure") {
					t.Fatalf("inventory failure = %v", err)
				}
			case "oversize":
				if !errors.Is(err, execpkg.ErrOutputTruncated) {
					t.Fatalf("inventory output bound = %v", err)
				}
			case "invalid":
				if !errors.Is(err, inference.ErrInventory) {
					t.Fatalf("inventory validation = %v", err)
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := InspectDevices(ctx, binary, "/fixture/mlx.metallib"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled inventory = %v", err)
	}
}
