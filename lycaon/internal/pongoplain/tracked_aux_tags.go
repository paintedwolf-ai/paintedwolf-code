package pongoplain

import (
	"fmt"
	"math"

	"github.com/flosch/pongo2/v6"
)

type trackedCompareBlockNode struct {
	var1, var2  pongo2.IEvaluator
	negate      bool
	thenWrapper *pongo2.NodeWrapper
	elseWrapper *pongo2.NodeWrapper
}

func (node *trackedCompareBlockNode) Execute(ctx *pongo2.ExecutionContext, writer pongo2.TemplateWriter) *pongo2.Error {
	state, stateErr := requireExecState(ctx)
	if stateErr != nil {
		return stateErr
	}
	state.step(1)
	r1, err := node.var1.Evaluate(ctx)
	if err != nil {
		return err
	}
	r2, err := node.var2.Evaluate(ctx)
	if err != nil {
		return err
	}
	matched := r1.EqualValueTo(r2)
	if node.negate {
		matched = !matched
	}
	if matched {
		return node.thenWrapper.Execute(ctx, writer)
	}
	if node.elseWrapper != nil {
		state.step(1)
		return node.elseWrapper.Execute(ctx, writer)
	}
	return nil
}

func parseTrackedCompareBlockTag(doc *pongo2.Parser, arguments *pongo2.Parser, endTag string, negate bool) (pongo2.INodeTag, *pongo2.Error) {
	var1, err := arguments.ParseExpression()
	if err != nil {
		return nil, err
	}
	var2, err := arguments.ParseExpression()
	if err != nil {
		return nil, err
	}
	if arguments.Remaining() > 0 {
		return nil, arguments.Error("ifequal only takes 2 arguments.", nil)
	}
	wrapper, endargs, err := doc.WrapUntilTag("else", endTag)
	if err != nil {
		return nil, err
	}
	if endargs.Count() > 0 {
		return nil, endargs.Error("Arguments not allowed here.", nil)
	}
	node := &trackedCompareBlockNode{var1: var1, var2: var2, negate: negate, thenWrapper: wrapper}
	if wrapper.Endtag == "else" {
		elseWrap, elseArgs, elseErr := doc.WrapUntilTag(endTag)
		if elseErr != nil {
			return nil, elseErr
		}
		if elseArgs.Count() > 0 {
			return nil, elseArgs.Error("Arguments not allowed here.", nil)
		}
		node.elseWrapper = elseWrap
	}
	return node, nil
}

func parseTrackedIfEqualTag(doc *pongo2.Parser, _ *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	return parseTrackedCompareBlockTag(doc, arguments, "endifequal", false)
}

func parseTrackedIfNotEqualTag(doc *pongo2.Parser, _ *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	return parseTrackedCompareBlockTag(doc, arguments, "endifnotequal", true)
}

type trackedFirstofNode struct {
	position *pongo2.Token
	args     []pongo2.IEvaluator
}

func (node *trackedFirstofNode) Execute(ctx *pongo2.ExecutionContext, writer pongo2.TemplateWriter) *pongo2.Error {
	state, stateErr := requireExecState(ctx)
	if stateErr != nil {
		return stateErr
	}
	state.step(1)
	for _, arg := range node.args {
		state.step(1)
		val, err := arg.Evaluate(ctx)
		if err != nil {
			return err
		}
		if val.IsTrue() {
			if ctx.Autoescape && !arg.FilterApplied("safe") {
				val, err = pongo2.ApplyFilter("escape", val, nil)
				if err != nil {
					return err
				}
			}
			if _, wErr := writer.WriteString(val.String()); wErr != nil {
				return ctx.OrigError(wErr, node.position)
			}
			return nil
		}
	}
	return nil
}

func parseTrackedFirstofTag(_ *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	node := &trackedFirstofNode{position: start}
	for arguments.Remaining() > 0 {
		expr, err := arguments.ParseExpression()
		if err != nil {
			return nil, err
		}
		node.args = append(node.args, expr)
	}
	return node, nil
}

type trackedWidthratioNode struct {
	position     *pongo2.Token
	current, max pongo2.IEvaluator
	width        pongo2.IEvaluator
	ctxName      string
}

func (node *trackedWidthratioNode) Execute(ctx *pongo2.ExecutionContext, writer pongo2.TemplateWriter) *pongo2.Error {
	state, stateErr := requireExecState(ctx)
	if stateErr != nil {
		return stateErr
	}
	state.step(1)
	current, err := node.current.Evaluate(ctx)
	if err != nil {
		return err
	}
	maxVal, err := node.max.Evaluate(ctx)
	if err != nil {
		return err
	}
	width, err := node.width.Evaluate(ctx)
	if err != nil {
		return err
	}
	maxF := maxVal.Float()
	if maxF == 0 {
		if node.ctxName != "" {
			ctx.Private[node.ctxName] = ""
		}
		return nil
	}
	value := int(math.Ceil(current.Float()/maxF*width.Float() + 0.5))
	if node.ctxName == "" {
		if _, wErr := writer.WriteString(fmt.Sprintf("%d", value)); wErr != nil {
			return ctx.OrigError(wErr, node.position)
		}
	} else {
		ctx.Private[node.ctxName] = value
	}
	return nil
}

func parseTrackedWidthratioTag(_ *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	current, err := arguments.ParseExpression()
	if err != nil {
		return nil, err
	}
	maxVal, err := arguments.ParseExpression()
	if err != nil {
		return nil, err
	}
	width, err := arguments.ParseExpression()
	if err != nil {
		return nil, err
	}
	node := &trackedWidthratioNode{position: start, current: current, max: maxVal, width: width}
	if arguments.MatchOne(pongo2.TokenKeyword, "as") != nil {
		nameToken := arguments.MatchType(pongo2.TokenIdentifier)
		if nameToken == nil {
			return nil, arguments.Error("Expected name (identifier).", nil)
		}
		node.ctxName = nameToken.Val
	}
	if arguments.Remaining() > 0 {
		return nil, arguments.Error("Malformed widthratio-tag arguments.", nil)
	}
	return node, nil
}

type trackedAutoescapeNode struct {
	wrapper    *pongo2.NodeWrapper
	autoescape bool
}

func (node *trackedAutoescapeNode) Execute(ctx *pongo2.ExecutionContext, writer pongo2.TemplateWriter) *pongo2.Error {
	state, stateErr := requireExecState(ctx)
	if stateErr != nil {
		return stateErr
	}
	state.step(1)
	old := ctx.Autoescape
	ctx.Autoescape = node.autoescape
	defer func() { ctx.Autoescape = old }()
	return node.wrapper.Execute(ctx, writer)
}

func parseTrackedAutoescapeTag(doc *pongo2.Parser, _ *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	wrapper, endargs, err := doc.WrapUntilTag("endautoescape")
	if err != nil {
		return nil, err
	}
	if endargs.Count() > 0 {
		return nil, endargs.Error("Arguments not allowed here.", nil)
	}
	modeToken := arguments.MatchType(pongo2.TokenIdentifier)
	if modeToken == nil {
		return nil, arguments.Error("A mode is required for autoescape-tag.", nil)
	}
	var autoescape bool
	switch modeToken.Val {
	case "on":
		autoescape = true
	case "off":
		autoescape = false
	default:
		return nil, arguments.Error("Only 'on' or 'off' is valid as an autoescape-mode.", nil)
	}
	if arguments.Remaining() > 0 {
		return nil, arguments.Error("Malformed autoescape-tag arguments.", nil)
	}
	return &trackedAutoescapeNode{wrapper: wrapper, autoescape: autoescape}, nil
}
