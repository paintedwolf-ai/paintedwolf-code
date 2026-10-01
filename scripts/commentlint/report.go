package main

import (
	"fmt"
	"sort"
	"time"
)

func printText(rep report) {
	for _, item := range rep.Findings {
		fmt.Printf("%s:%d:%d: %s: %s [%s/%s]\n",
			item.Path, item.Line, item.Column, item.Rule, item.Message, item.Language, item.Kind)
	}
	fmt.Printf("\n%d findings in %d comments across %d scanned files", len(rep.Findings), rep.Comments, rep.FilesScanned)
	fmt.Printf(" (%d syntax-validated, %d unsupported, %d skipped, %d timed out) in %s\n",
		rep.FilesValidated, rep.FilesUnsupported, rep.FilesSkipped, len(rep.ParseTimeouts),
		time.Duration(rep.ElapsedMilliseconds)*time.Millisecond)
	for _, path := range rep.ParseTimeouts {
		fmt.Printf("timeout: %s\n", path)
	}
	names := make([]string, 0, len(rep.Languages))
	for name := range rep.Languages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		stats := rep.Languages[name]
		fmt.Printf("language %s: %d files, %d scanned, %d syntax-validated, %d timed out\n",
			name, stats.Files, stats.Scanned, stats.Validated, stats.Timeouts)
	}
}
