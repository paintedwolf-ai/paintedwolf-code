package boolexpr

// Node is a whitelisted boolean expression AST for hint when and rule expressions.
type Node interface {
	node()
}

type Ident struct {
	Name string
}

func (Ident) node() {}

type Not struct {
	Expr Node
}

func (Not) node() {}

type And struct {
	Left, Right Node
}

func (And) node() {}

type Or struct {
	Left, Right Node
}

func (Or) node() {}
