package syntaxhealth

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tsparse"
)

func TestGoEndOfFileSyntax(t *testing.T) {
	cases := map[string]string{
		"package":            "package models",
		"struct":             "package models\n\ntype Post struct { ID int64 }",
		"function":           "package main\n\nfunc main() {}",
		"comment":            "package models\n\nvar ID = 1 // last line",
		"embed":              "package migrations\n\nimport \"embed\"\n\n//go:embed *.sql\nvar FS embed.FS",
		"unclosed struct":    "package models\n\ntype Post struct { ID int64",
		"unclosed string":    "package models\n\nvar Name = \"broken",
		"missing expression": "package models\n\nvar ID =",
	}
	for _, name := range []string{"embed", "models"} {
		src, err := os.ReadFile("testdata/go-eof/" + name + ".go")
		testutil.FailErr(t, "read captured source", err)
		cases["captured "+name] = string(src)
	}
	for name, source := range cases {
		for ending, suffix := range map[string]string{"eof": "", "lf": "\n", "crlf": "\r\n"} {
			t.Run(name+"/"+ending, func(t *testing.T) {
				src := []byte(source + suffix)
				_, parseErr := parser.ParseFile(token.NewFileSet(), "source.go", src, parser.AllErrors)
				want := StatusClean
				if parseErr != nil {
					want = StatusBroken
				}
				got := Analyze(context.Background(), "", "source.go", src, tsparse.Validation)
				if got.Status != want {
					t.Fatalf("status = %s, want %s (Go parser: %v); diagnostics: %+v", got.Status, want, parseErr, got.Diagnostics)
				}
				for _, diagnostic := range got.Diagnostics {
					if diagnostic.StartByte > len(src) || diagnostic.EndByte > len(src) {
						t.Fatalf("diagnostic outside original source: %+v", diagnostic)
					}
				}
			})
		}
	}
}
