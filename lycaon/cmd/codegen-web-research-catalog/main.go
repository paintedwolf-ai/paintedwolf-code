// Command codegen-web-research-catalog emits Den catalog types, the OpenAPI
// WebSearchProvider enum fragment, and pkg/api provider id consts from
// web-research-providers.yaml.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/webresearch"
)

func main() {
	outTS := flag.String("out-ts", "", "output path for web-research-catalog.generated.ts")
	outOpenAPI := flag.String("out-openapi", "", "output path for web-search-provider.generated.yaml")
	outGo := flag.String("out-go", "", "output path for webresearch_provider_ids.generated.go")
	check := flag.Bool("check", false, "exit 1 if any output is stale")
	flag.Parse()

	if *outTS == "" || *outOpenAPI == "" || *outGo == "" {
		fatal(fmt.Errorf("--out-ts, --out-openapi, and --out-go are required"))
	}

	cat, err := webresearch.LoadCatalog()
	if err != nil {
		fatal(err)
	}

	tsBody, err := renderTypeScript(cat)
	if err != nil {
		fatal(err)
	}
	openapiBody, err := renderOpenAPIEnum(cat)
	if err != nil {
		fatal(err)
	}
	goBody, err := renderGoProviderIDs(cat)
	if err != nil {
		fatal(err)
	}

	outputs := []struct {
		path string
		body []byte
		hint string
	}{
		{*outTS, tsBody, "Den catalog TS"},
		{*outOpenAPI, openapiBody, "OpenAPI WebSearchProvider enum"},
		{*outGo, goBody, "pkg/api provider ids"},
	}

	if *check {
		for _, out := range outputs {
			existing, err := os.ReadFile(out.path) // #nosec G304 -- path from Taskfile
			if err != nil || string(existing) != string(out.body) {
				fatal(fmt.Errorf("stale %s (%s) — run ./task codegen:web-research-catalog", out.path, out.hint))
			}
		}
		fmt.Println("web research catalog codegen OK")
		return
	}

	for _, out := range outputs {
		if err := os.MkdirAll(filepath.Dir(out.path), 0o750); err != nil {
			fatal(err)
		}
		if err := os.WriteFile(out.path, out.body, 0o600); err != nil { // #nosec G306 -- generated artifact
			fatal(err)
		}
		fmt.Println("wrote", out.path)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
