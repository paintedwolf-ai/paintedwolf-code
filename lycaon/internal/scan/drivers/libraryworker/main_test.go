package libraryworker

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/scanworker"
)

func TestMain(m *testing.M) {
	for _, arg := range os.Args[1:] {
		if arg == "internal-scan-worker" {
			if err := scanworker.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}
