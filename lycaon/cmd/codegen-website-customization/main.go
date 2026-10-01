// Command codegen-website-customization projects the bundled workflow catalog
// (lycaon/config/packs/painted-wolf/platform/workflows) plus paintedwolf-www/data/customization.overlay.yaml
// into the Hugo data/customization.yaml that drives the "Customizing behavior" docs.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/website"
)

func main() {
	root := flag.String("root", ".", "lycaon module root (contains config/)")
	overlay := flag.String("overlay", "", "path to data/customization.overlay.yaml")
	out := flag.String("out", "", "output path for data/customization.yaml")
	check := flag.Bool("check", false, "exit 1 if output is stale")
	flag.Parse()

	if *overlay == "" || *out == "" {
		fatal(fmt.Errorf("--overlay and --out are required"))
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fatal(err)
	}
	body, err := website.RenderCustomizationYAML(absRoot, *overlay)
	if err != nil {
		fatal(err)
	}

	if *check {
		existing, err := os.ReadFile(*out) // #nosec G304 -- path from caller (www sync script)
		if err != nil || string(existing) != string(body) {
			fatal(fmt.Errorf("stale %s — run ./task customization:sync", *out))
		}
		fmt.Println("customization sync OK")
		return
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out, body, 0o600); err != nil { // #nosec G306 -- generated artifact
		fatal(err)
	}
	fmt.Println("wrote", *out)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
