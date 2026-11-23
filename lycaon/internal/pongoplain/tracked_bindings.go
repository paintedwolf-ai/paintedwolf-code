package pongoplain

import "github.com/flosch/pongo2/v6"

type bindingPair struct {
	key  string
	expr pongo2.IEvaluator
}

type trackedSetNode struct {
	name       string
	expression pongo2.IEvaluator
}

func (node *trackedSetNode) Execute(ctx *pongo2.ExecutionContext, _ pongo2.TemplateWriter) *pongo2.Error {
	state, stateErr := requireExecState(ctx)
	if stateErr != nil {
		return stateErr
	}
	state.step(1)
	value, err := node.expression.Evaluate(ctx)
	if err != nil {
		return err
	}
	ctx.Private[node.name] = value
	return nil
}

func parseTrackedSetTag(_ *pongo2.Parser, _ *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	typeToken := arguments.MatchType(pongo2.TokenIdentifier)
	if typeToken == nil {
		return nil, arguments.Error("Expected an identifier.", nil)
	}
	if arguments.Match(pongo2.TokenSymbol, "=") == nil {
		return nil, arguments.Error("Expected '='.", nil)
	}
	expr, err := arguments.ParseExpression()
	if err != nil {
		return nil, err
	}
	if arguments.Remaining() > 0 {
		return nil, arguments.Error("Malformed 'set'-tag arguments.", nil)
	}
	return &trackedSetNode{name: typeToken.Val, expression: expr}, nil
}

type trackedWithNode struct {
	pairs   []bindingPair
	wrapper *pongo2.NodeWrapper
}

func (node *trackedWithNode) Execute(ctx *pongo2.ExecutionContext, writer pongo2.TemplateWriter) *pongo2.Error {
	state, stateErr := requireExecState(ctx)
	if stateErr != nil {
		return stateErr
	}
	state.step(1)
	withCtx := pongo2.NewChildExecutionContext(ctx)
	for _, pair := range node.pairs {
		state.step(1)
		val, err := pair.expr.Evaluate(ctx)
		if err != nil {
			return err
		}
		withCtx.Private[pair.key] = val
	}
	return node.wrapper.Execute(withCtx, writer)
}

func parseTrackedWithTag(doc *pongo2.Parser, _ *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	if arguments.Count() == 0 {
		return nil, arguments.Error("Tag 'with' requires at least one argument.", nil)
	}
	wrapper, endargs, err := doc.WrapUntilTag("endwith")
	if err != nil {
		return nil, err
	}
	if endargs.Count() > 0 {
		return nil, endargs.Error("Arguments not allowed here.", nil)
	}
	oldStyle := false
	for i := 0; i < arguments.Count(); i++ {
		if arguments.PeekN(i, pongo2.TokenKeyword, "as") != nil {
			oldStyle = true
			break
		}
	}
	withNode := &trackedWithNode{wrapper: wrapper}
	for arguments.Remaining() > 0 {
		if oldStyle {
			valueExpr, exprErr := arguments.ParseExpression()
			if exprErr != nil {
				return nil, exprErr
			}
			if arguments.Match(pongo2.TokenKeyword, "as") == nil {
				return nil, arguments.Error("Expected 'as' keyword.", nil)
			}
			keyToken := arguments.MatchType(pongo2.TokenIdentifier)
			if keyToken == nil {
				return nil, arguments.Error("Expected an identifier", nil)
			}
			withNode.pairs = append(withNode.pairs, bindingPair{key: keyToken.Val, expr: valueExpr})
		} else {
			keyToken := arguments.MatchType(pongo2.TokenIdentifier)
			if keyToken == nil {
				return nil, arguments.Error("Expected an identifier", nil)
			}
			if arguments.Match(pongo2.TokenSymbol, "=") == nil {
				return nil, arguments.Error("Expected '='.", nil)
			}
			valueExpr, exprErr := arguments.ParseExpression()
			if exprErr != nil {
				return nil, exprErr
			}
			withNode.pairs = append(withNode.pairs, bindingPair{key: keyToken.Val, expr: valueExpr})
		}
	}
	return withNode, nil
}
