package search

// Node is the root of a parsed search DSL expression tree.
type Node interface {
	node()
}

type (
	// AndExpr combines subtrees with AND (explicit or implicit).
	AndExpr struct {
		Exprs []Node
	}
	// OrExpr combines subtrees with OR.
	OrExpr struct {
		Exprs []Node
	}
	// NotExpr negates one subtree.
	NotExpr struct {
		Expr Node
	}
	// FilterExpr is one allowlisted field filter.
	FilterExpr struct {
		Field  string
		Value  string
		Offset int
	}
	// Phrase terms match one contiguous string, including spaces.
	TextExpr struct {
		Text   string
		Offset int
		Phrase bool
	}
)

func (AndExpr) node()    {}
func (OrExpr) node()     {}
func (NotExpr) node()    {}
func (FilterExpr) node() {}
func (TextExpr) node()   {}
