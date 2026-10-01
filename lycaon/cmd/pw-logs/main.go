// Shipped capture log viewer beside pw; TUI stays out of the Den sidecar.
package main

import (
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/logscli"
)

func main() {
	if err := logscli.Run("pw-logs", os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "pw-logs: %v\n", err)
		os.Exit(1)
	}
}
