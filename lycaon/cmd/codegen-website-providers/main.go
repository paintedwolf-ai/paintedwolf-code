// Command codegen-website-providers merges lycaon/config/packs/painted-wolf/platform/host/providers.yaml
// with paintedwolf-www/data/providers.overlay.yaml for Hugo site data.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/website"
)

func main() {
	overlay := flag.String("overlay", "", "path to data/providers.overlay.yaml")
	out := flag.String("out", "", "output path for data/providers.yaml")
	check := flag.Bool("check", false, "exit 1 if output is stale")
	flag.Parse()

	if *overlay == "" || *out == "" {
		fatal(fmt.Errorf("--overlay and --out are required"))
	}

	body, err := website.RenderProvidersYAML(*overlay)
	if err != nil {
		fatal(err)
	}

	if *check {
		existing, err := os.ReadFile(*out) // #nosec G304 -- path from caller (www sync script)
		if err != nil || string(existing) != string(body) {
			fatal(fmt.Errorf("stale %s — run ./task providers:sync", *out))
		}
		fmt.Println("providers sync OK")
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
