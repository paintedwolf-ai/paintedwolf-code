package pongoplain

import (
	"fmt"

	"github.com/flosch/pongo2/v6"
)

func installTrackedControlTags() {
	for _, entry := range []struct {
		name   string
		parser pongo2.TagParser
	}{
		{"if", parseTrackedIfTag},
		{"for", parseTrackedForTag},
		{"include", parseTrackedIncludeTag},
		{"set", parseTrackedSetTag},
		{"with", parseTrackedWithTag},
		{"ifequal", parseTrackedIfEqualTag},
		{"ifnotequal", parseTrackedIfNotEqualTag},
		{"firstof", parseTrackedFirstofTag},
		{"widthratio", parseTrackedWidthratioTag},
		{"autoescape", parseTrackedAutoescapeTag},
	} {
		if err := pongo2.ReplaceTag(entry.name, entry.parser); err != nil {
			panic(fmt.Sprintf("pongoplain: install tracked %s tag: %v", entry.name, err))
		}
	}
}

type branchSite struct {
	filename string
	line     int
	col      int
	kind     BranchKind
}

type trackedIfNode struct {
	conditions []pongo2.IEvaluator
	arms       []*conditionArm
	sites      []branchSite
	wrappers   []*pongo2.NodeWrapper
	elseSite   *branchSite
}

func (node *trackedIfNode) Execute(ctx *pongo2.ExecutionContext, writer pongo2.TemplateWriter) *pongo2.Error {
	state, stateErr := requireExecState(ctx)
	if stateErr != nil {
		return stateErr
	}
	rec := ActiveBranchRecorder.Load()
	if rec != nil && isAtomReplay(ctx) {
		rec = nil
	}
	for i, condition := range node.conditions {
		state.step()
		result, err := condition.Evaluate(ctx)
		if err != nil {
			return err
		}
		taken := result.IsTrue()
		if rec != nil {
			site := node.sites[i]
			rec.record(site.filename, site.line, site.col, site.kind, taken)
			if rec.ObserveConditions {
				obs := node.arms[i].observe(ctx, taken, rec.Partition)
				obs.path = state.includePath
				rec.recordCondition(site, node.arms[i].source, obs)
			}
		}
		if taken {
			if rec != nil && node.elseSite != nil {
				rec.record(node.elseSite.filename, node.elseSite.line, node.elseSite.col, BranchElse, false)
			}
			return node.wrappers[i].Execute(ctx, writer)
		}
		if len(node.conditions) == i+1 && len(node.wrappers) > i+1 {
			state.step()
			if rec != nil && node.elseSite != nil {
				rec.record(node.elseSite.filename, node.elseSite.line, node.elseSite.col, BranchElse, true)
			}
			return node.wrappers[i+1].Execute(ctx, writer)
		}
	}
	return nil
}

func siteFromToken(tok *pongo2.Token, kind BranchKind) branchSite {
	return branchSite{
		filename: tok.Filename,
		line:     tok.Line,
		col:      tok.Col,
		kind:     kind,
	}
}

func parseTrackedIfTag(doc *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	ifNode := &trackedIfNode{}
	ifNode.arms = append(ifNode.arms, &conditionArm{source: tokenSource(arguments)})
	condition, err := arguments.ParseExpression()
	if err != nil {
		return nil, err
	}
	if arguments.Remaining() > 0 {
		return nil, arguments.Error("If-condition is malformed.", nil)
	}
	ifNode.conditions = append(ifNode.conditions, condition)
	ifNode.sites = append(ifNode.sites, siteFromToken(start, BranchIf))

	for {
		wrapper, tagArgs, wrapErr := doc.WrapUntilTag("elif", "else", "endif")
		if wrapErr != nil {
			return nil, wrapErr
		}
		ifNode.wrappers = append(ifNode.wrappers, wrapper)
		endTok := doc.GetR(-(tagArgs.Count() + 2))

		if wrapper.Endtag == "elif" {
			ifNode.arms = append(ifNode.arms, &conditionArm{source: tokenSource(tagArgs)})
			elifCond, condErr := tagArgs.ParseExpression()
			if condErr != nil {
				return nil, condErr
			}
			if tagArgs.Remaining() > 0 {
				return nil, tagArgs.Error("Elif-condition is malformed.", nil)
			}
			ifNode.conditions = append(ifNode.conditions, elifCond)
			ifNode.sites = append(ifNode.sites, siteFromToken(endTok, BranchElif))
		} else {
			if tagArgs.Count() > 0 {
				return nil, tagArgs.Error("Arguments not allowed here.", nil)
			}
			if wrapper.Endtag == "else" {
				site := siteFromToken(endTok, BranchElse)
				ifNode.elseSite = &site
			}
		}

		if wrapper.Endtag == "endif" {
			break
		}
	}
	return ifNode, nil
}

type trackedForNode struct {
	key             string
	value           string
	objectEvaluator pongo2.IEvaluator
	reversed        bool
	sorted          bool
	forSite         branchSite
	emptySite       *branchSite
	bodyWrapper     *pongo2.NodeWrapper
	emptyWrapper    *pongo2.NodeWrapper
}

type trackedForLoopInfo struct {
	Counter     int
	Counter0    int
	Revcounter  int
	Revcounter0 int
	First       bool
	Last        bool
	Parentloop  *trackedForLoopInfo
}

func (node *trackedForNode) Execute(ctx *pongo2.ExecutionContext, writer pongo2.TemplateWriter) (forError *pongo2.Error) {
	state, stateErr := requireExecState(ctx)
	if stateErr != nil {
		return stateErr
	}
	state.forDepth++
	defer func() { state.forDepth-- }()
	if state.forDepth > MaxForNesting {
		panic(executionAbort{err: fmt.Errorf("%w: for nesting exceeds %d", ErrExecutionLimit, MaxForNesting)})
	}
	state.step()

	rec := ActiveBranchRecorder.Load()
	forCtx := pongo2.NewChildExecutionContext(ctx)
	loopInfo := &trackedForLoopInfo{First: true}
	if parent, ok := forCtx.Private["forloop"].(*trackedForLoopInfo); ok {
		loopInfo.Parentloop = parent
	}
	forCtx.Private["forloop"] = loopInfo

	obj, err := node.objectEvaluator.Evaluate(forCtx)
	if err != nil {
		return err
	}

	entered := false
	obj.IterateOrder(func(idx, count int, key, value *pongo2.Value) bool {
		state.step()
		entered = true
		forCtx.Private[node.key] = key
		if value != nil && node.value != "" {
			forCtx.Private[node.value] = value
		}
		loopInfo.Counter = idx + 1
		loopInfo.Counter0 = idx
		if idx == 1 {
			loopInfo.First = false
		}
		if idx+1 == count {
			loopInfo.Last = true
		}
		loopInfo.Revcounter = count - idx
		loopInfo.Revcounter0 = count - (idx + 1)

		if execErr := node.bodyWrapper.Execute(forCtx, writer); execErr != nil {
			forError = execErr
			return false
		}
		return true
	}, func() {
		if node.emptyWrapper != nil {
			state.step()
			if execErr := node.emptyWrapper.Execute(forCtx, writer); execErr != nil {
				forError = execErr
			}
		}
	}, node.reversed, node.sorted)

	if rec != nil {
		rec.record(node.forSite.filename, node.forSite.line, node.forSite.col, BranchFor, entered)
		if node.emptySite != nil {
			rec.record(node.emptySite.filename, node.emptySite.line, node.emptySite.col, BranchEmpty, !entered)
		}
	}
	return forError
}

func parseTrackedForTag(doc *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	forNode := &trackedForNode{forSite: siteFromToken(start, BranchFor)}
	keyToken := arguments.MatchType(pongo2.TokenIdentifier)
	if keyToken == nil {
		return nil, arguments.Error("Expected an key identifier as first argument for 'for'-tag", nil)
	}
	var valueToken *pongo2.Token
	if arguments.Match(pongo2.TokenSymbol, ",") != nil {
		valueToken = arguments.MatchType(pongo2.TokenIdentifier)
		if valueToken == nil {
			return nil, arguments.Error("Value name must be an identifier.", nil)
		}
	}
	if arguments.Match(pongo2.TokenKeyword, "in") == nil {
		return nil, arguments.Error("Expected keyword 'in'.", nil)
	}
	objectEvaluator, err := arguments.ParseExpression()
	if err != nil {
		return nil, err
	}
	forNode.objectEvaluator = objectEvaluator
	forNode.key = keyToken.Val
	if valueToken != nil {
		forNode.value = valueToken.Val
	}
	if arguments.MatchOne(pongo2.TokenIdentifier, "reversed") != nil {
		forNode.reversed = true
	}
	if arguments.MatchOne(pongo2.TokenIdentifier, "sorted") != nil {
		forNode.sorted = true
	}
	if arguments.Remaining() > 0 {
		return nil, arguments.Error("Malformed for-loop arguments.", nil)
	}

	wrapper, endargs, wrapErr := doc.WrapUntilTag("empty", "endfor")
	if wrapErr != nil {
		return nil, wrapErr
	}
	if endargs.Count() > 0 {
		return nil, endargs.Error("Arguments not allowed here.", nil)
	}
	forNode.bodyWrapper = wrapper

	if wrapper.Endtag == "empty" {
		emptyTok := doc.GetR(-2)
		site := siteFromToken(emptyTok, BranchEmpty)
		forNode.emptySite = &site
		emptyWrap, emptyEndArgs, emptyErr := doc.WrapUntilTag("endfor")
		if emptyErr != nil {
			return nil, emptyErr
		}
		if emptyEndArgs.Count() > 0 {
			return nil, emptyEndArgs.Error("Arguments not allowed here.", nil)
		}
		forNode.emptyWrapper = emptyWrap
	}
	return forNode, nil
}
