// Command oar-import-invariant converts an Invariant-subset rule file into OAR hint YAML.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
)

func main() {
	in := flag.String("in", "", "path to Invariant-subset rule (.inv / .txt)")
	out := flag.String("out", "", "output hint YAML path (default: <id>.yaml in cwd)")
	flag.Parse()
	if strings.TrimSpace(*in) == "" {
		fatal(fmt.Errorf("required: -in <path>"))
	}
	raw, err := os.ReadFile(*in) // #nosec G304 -- operator-selected input path
	if err != nil {
		fatal(err)
	}
	imp, err := oar.ImportInvariantSubset(string(raw))
	if err != nil {
		fatal(err)
	}
	yamlBytes, err := imp.ToHintYAML()
	if err != nil {
		fatal(err)
	}
	dest := *out
	if dest == "" {
		dest = imp.ID + ".yaml"
	}
	if dir := filepath.Dir(dest); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil { // #nosec G703 -- operator-selected output dir
			fatal(err)
		}
	}
	if err := os.WriteFile(dest, yamlBytes, 0o600); err != nil { // #nosec G703 -- operator-selected output path
		fatal(err)
	}
	fmt.Printf("wrote %s (id=%s)\n", dest, imp.ID)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "oar-import-invariant: %v\n", err)
	os.Exit(1)
}
