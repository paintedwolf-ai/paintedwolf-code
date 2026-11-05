// Command next-step-report reads transcripts as JSON arrays of messages, one file
// per session, and prints how the next steps they carry fared.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/nextstepreport"
	"github.com/lycaon/lycaon/pkg/api"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(paths []string) error {
	var report nextstepreport.Report
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var messages []api.Message
		if err := json.Unmarshal(raw, &messages); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		report.Add(messages)
	}
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	return out.Encode(report)
}
