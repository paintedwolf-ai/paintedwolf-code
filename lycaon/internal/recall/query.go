package recall

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/search"
)

// liveCodeKinds read the working tree. Serving them here would put two
// freshness contracts under one result shape with no way to tell them apart.
var liveCodeKinds = map[string]struct{}{
	search.HitKindCode: {},
	search.HitKindFile: {},
}

// ErrLiveCodeRequested is returned when a query asks for the working tree.
type ErrLiveCodeRequested struct{ Kind string }

func (e *ErrLiveCodeRequested) Error() string {
	return fmt.Sprintf("recall: kind:%s reads the working tree, not the record", e.Kind)
}

// validateQuery parses ahead of the compiler so an out-of-scope kind rejects
// with a code the caller can branch on rather than a generic parse error.
func validateQuery(query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return fmt.Errorf("recall: empty query")
	}
	ast, err := search.ParseQuery(query)
	if err != nil {
		return err
	}
	return walkForLiveCode(ast)
}

func walkForLiveCode(n search.Node) error {
	switch v := n.(type) {
	case search.AndExpr:
		return walkChildren(v.Exprs)
	case search.OrExpr:
		return walkChildren(v.Exprs)
	case search.NotExpr:
		// A negated code kind still says the caller believes it is searching the tree.
		return walkForLiveCode(v.Expr)
	case search.FilterExpr:
		if !strings.EqualFold(strings.TrimSpace(v.Field), "kind") {
			return nil
		}
		kind := strings.ToLower(strings.TrimSpace(v.Value))
		if _, ok := liveCodeKinds[kind]; ok {
			return &ErrLiveCodeRequested{Kind: kind}
		}
	}
	return nil
}

func walkChildren(children []search.Node) error {
	for _, child := range children {
		if err := walkForLiveCode(child); err != nil {
			return err
		}
	}
	return nil
}
