package workflowdiag

import (
	"fmt"
	"sort"
)

// AllCodes returns every code from the default catalog, sorted for stability.
func AllCodes() []Code {
	cat, err := Default()
	if err != nil {
		panic(fmt.Sprintf("workflowdiag: load default catalog: %v", err))
	}
	return cat.Codes()
}

// Codes returns catalog keys sorted by string value.
func (c *Catalog) Codes() []Code {
	if c == nil {
		return nil
	}
	out := make([]Code, 0, len(c.entries))
	for code := range c.entries {
		out = append(out, code)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
