// Command codegen-website-sdk projects the engine extension vocabularies
// (workflow gate kit, extpacks unit kinds, stock pack inventory, diagnostic
// codes) plus paintedwolf-www/data/sdk.overlay.yaml into the Hugo data/sdk.yaml
// that drives the website SDK docs.
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
	overlay := flag.String("overlay", "", "path to data/sdk.overlay.yaml")
	out := flag.String("out", "", "output path for data/sdk.yaml")
	check := flag.Bool("check", false, "exit 1 if output is stale")
	flag.Parse()

	if *overlay == "" || *out == "" {
		fatal(fmt.Errorf("--overlay and --out are required"))
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fatal(err)
	}
	body, err := website.RenderSDKYAML(absRoot, *overlay)
	if err != nil {
		fatal(err)
	}

	if *check {
		existing, err := os.ReadFile(*out) // #nosec G304 -- path from caller (www sync script)
		if err != nil || string(existing) != string(body) {
			fatal(fmt.Errorf("stale %s — run ./task sdk:sync", *out))
		}
		fmt.Println("sdk sync OK")
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
