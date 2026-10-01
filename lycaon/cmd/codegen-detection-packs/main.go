// Command codegen-detection-packs generates structured cloud rules.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	root := flag.String("root", ".", "lycaon module root (contains config/)")
	check := flag.Bool("check", false, "regenerate into a temp dir and byte-diff against the committed packs")
	flag.Parse()

	paths, err := resolveModulePaths(*root)
	if err != nil {
		fatal(err)
	}

	if *check {
		if err := checkGeneratedPacks(paths); err != nil {
			fatal(err)
		}
		return
	}

	bridges, err := generateStructuredCloudPacks(paths.structuredCloudMap, paths.root)
	if err != nil {
		fatal(err)
	}
	for _, bridge := range bridges {
		fmt.Printf("  %s: %d rules, %d operation identities\n", bridge.PackID, bridge.RuleCount, bridge.OperationCount)
	}
	fmt.Println("wrote structured cloud detection packs")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
