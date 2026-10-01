// Command oar-conformance runs the shared corpus through the production engine.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/oar"
	"os"
)

func main() {
	jsonMode, dir := false, ""
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--json":
			jsonMode = true
		case "-h", "--help":
			fmt.Fprintln(os.Stdout, "usage: oar-conformance [--json] [<corpus-dir>]")
			return
		default:
			if dir != "" {
				fmt.Fprintln(os.Stderr, "oar-conformance: more than one corpus directory")
				os.Exit(2)
			}
			dir = arg
		}
	}
	if dir == "" {
		dir = oar.FindCorpusDir("")
	}
	outcomes, err := oar.RunCorpusDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "oar-conformance: %v\n", err)
		os.Exit(2)
	}
	if len(outcomes) == 0 {
		fmt.Fprintln(os.Stderr, "oar-conformance: corpus contains no fixtures")
		os.Exit(2)
	}
	passed, failed, skipped := 0, 0, 0
	results := make([]map[string]any, 0, len(outcomes))
	for _, result := range outcomes {
		switch result.Status {
		case "pass":
			passed++
		case "skip":
			skipped++
		default:
			failed++
		}
		results = append(results, map[string]any{"id": result.ID, "pass": result.Status == "pass", "skipped": result.Status == "skip", "detail": result.Reason, "actual": result.Actual})
		if !jsonMode {
			fmt.Fprintf(os.Stdout, "%s %s %s\n", result.Status, result.ID, result.Reason)
		}
	}
	if jsonMode {
		err = json.NewEncoder(os.Stdout).Encode(map[string]any{"passed": passed, "failed": failed, "skipped": skipped, "results": results})
		if err != nil {
			fmt.Fprintf(os.Stderr, "oar-conformance: %v\n", err)
			os.Exit(2)
		}
	} else {
		fmt.Fprintf(os.Stdout, "%d passed; %d failed; %d skipped\n", passed, failed, skipped)
	}
	if failed != 0 {
		os.Exit(1)
	}
}
