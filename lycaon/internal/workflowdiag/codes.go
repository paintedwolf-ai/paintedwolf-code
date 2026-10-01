package workflowdiag

import "fmt"

// Code is a workflow-diagnostics catalog id (YAML `code:` / filename stem).
// Catalog YAML under packs/.../workflow-diagnostics/ alone defines which codes
// exist.
type Code string

// MustCode returns Code(s) after checking it is registered in the default
// catalog. Panics if the pack catalog cannot load or s is unknown — use at
// emit sites so typos fail closed.
func MustCode(s string) Code {
	c := Code(s)
	cat, err := Default()
	if err != nil {
		panic(fmt.Sprintf("workflowdiag: load default catalog: %v", err))
	}
	if !cat.Has(c) {
		panic(fmt.Sprintf("workflowdiag: unknown code %q (not in workflow-diagnostics catalog)", s))
	}
	return c
}

// AllCodes returns every code from the default catalog, sorted for stability.
func AllCodes() []Code {
	cat, err := Default()
	if err != nil {
		panic(fmt.Sprintf("workflowdiag: load default catalog: %v", err))
	}
	return cat.Codes()
}
