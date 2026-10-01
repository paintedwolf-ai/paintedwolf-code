package testcorpus_test

import (
	"fmt"
	"go/ast"
	"go/parser"

	"github.com/lycaon/lycaon/pkg/testcorpus"
)

func ExampleLoadGo() {
	corpus, err := testcorpus.LoadGo(".", testcorpus.Options{}, parser.SkipObjectResolution)
	if err != nil {
		panic(err)
	}
	declarations := 0
	for _, file := range corpus.Production() {
		ast.Inspect(file.AST, func(node ast.Node) bool {
			if _, ok := node.(*ast.FuncDecl); ok {
				declarations++
			}
			return true
		})
	}
	fmt.Println(declarations > 0)
	// Output: true
}
