// Command codegen-tool-command-equivalence generates the frozen command
// language catalog from its source JSON.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
)

func main() {
	root := flag.String("root", ".", "lycaon module root (contains config/)")
	check := flag.Bool("check", false, "exit 1 if generated outputs are stale")
	flag.Parse()

	paths, err := resolveModulePaths(*root)
	if err != nil {
		fatal(err)
	}
	if err := generateLanguageCatalog(paths.root, *check); err != nil {
		fatal(err)
	}
}

func writeOrCheck(path string, content []byte, check bool) error {
	if check {
		existing, err := readTrackedFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w (run ./task codegen:tool-command-equivalence)", path, err)
		}
		if !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(content)) {
			return fmt.Errorf("%s is stale — run ./task codegen:tool-command-equivalence", path)
		}
		return nil
	}
	return writeTrackedFile(path, content)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "codegen-tool-command-equivalence:", err)
	os.Exit(1)
}
