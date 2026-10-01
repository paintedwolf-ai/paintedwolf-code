// Command tsquery generates type-safe Go code from tree-sitter .scm query files.
//
// Usage:
//
//	tsquery -input queries/go_functions.scm -lang go -output go_functions_query.go -package queries
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	input := flag.String("input", "", "path to .scm query file")
	lang := flag.String("lang", "", "language name (for documentation)")
	output := flag.String("output", "", "output Go file path")
	pkg := flag.String("package", "", "Go package name for generated file")
	name := flag.String("name", "", "override for query type name (default: derived from filename)")
	flag.Parse()

	if *input == "" || *output == "" || *pkg == "" {
		fmt.Fprintln(os.Stderr, "usage: tsquery -input FILE.scm -lang LANG -output FILE.go -package PKG")
		flag.PrintDefaults()
		os.Exit(1)
	}

	querySource, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", *input, err)
		os.Exit(1)
	}

	typeName := *name
	if typeName == "" {
		base := filepath.Base(*input)
		base = strings.TrimSuffix(base, filepath.Ext(base))
		typeName = toPascalCase(base)
	}

	code, err := Generate(string(querySource), typeName, *pkg, *lang)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error generating code: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*output, []byte(code), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing %s: %v\n", *output, err)
		os.Exit(1)
	}
}
