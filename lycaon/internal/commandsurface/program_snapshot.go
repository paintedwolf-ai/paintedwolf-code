package commandsurface

func cloneProgram(p ProgramSnapshot) ProgramSnapshot {
	p.Statements = cloneStatements(p.Statements)
	return p
}
func cloneStatements(src []Statement) []Statement {
	out := make([]Statement, len(src))
	for i, s := range src {
		out[i] = cloneStatement(s)
	}
	return out
}
func cloneStatement(s Statement) Statement {
	s.Command = cloneCommand(s.Command)
	s.Redirects = append([]ProgramRedirect(nil), s.Redirects...)
	for i := range s.Redirects {
		r := &s.Redirects[i]
		r.Target = cloneWord(r.Target)
		if r.HereDocument != nil {
			word := cloneWord(*r.HereDocument)
			r.HereDocument = &word
		}
	}
	return s
}
func cloneCommand(cmd CommandNode) CommandNode {
	switch v := cmd.(type) {
	case SimpleCommand:
		v.Assignments = cloneAssignments(v.Assignments)
		v.Words = cloneWords(v.Words)
		return v
	case EnvironmentDeclaration:
		v.Assignments = cloneAssignments(v.Assignments)
		v.Words = cloneWords(v.Words)
		return v
	case StatementGroup:
		v.Statements = cloneStatements(v.Statements)
		return v
	case BinaryCommand:
		v.Left = cloneStatement(v.Left)
		v.Right = cloneStatement(v.Right)
		return v
	case Conditional:
		return cloneConditional(v)
	case WhileLoop:
		v.Condition = cloneStatements(v.Condition)
		v.Body = cloneStatements(v.Body)
		return v
	case ForLoop:
		v.Items = cloneWords(v.Items)
		v.Body = cloneStatements(v.Body)
		return v
	case FunctionDeclaration:
		v.Body = cloneStatement(v.Body)
		return v
	case TestExpression:
		v.Expression = cloneTest(v.Expression)
		return v
	default:
		return nil
	}
}
func cloneConditional(v Conditional) Conditional {
	v.Condition = cloneStatements(v.Condition)
	v.Then = cloneStatements(v.Then)
	if v.Else != nil {
		next := cloneConditional(*v.Else)
		v.Else = &next
	}
	return v
}
func cloneAssignments(src []Assignment) []Assignment {
	out := append([]Assignment(nil), src...)
	for i := range out {
		out[i].Value = cloneWord(out[i].Value)
	}
	return out
}
func cloneWords(src []DeferredWord) []DeferredWord {
	out := make([]DeferredWord, len(src))
	for i, w := range src {
		out[i] = cloneWord(w)
	}
	return out
}
func cloneWord(w DeferredWord) DeferredWord { w.Parts = cloneParts(w.Parts); return w }
func cloneParts(src []WordNode) []WordNode {
	out := make([]WordNode, len(src))
	for i, part := range src {
		switch p := part.(type) {
		case DoubleQuotedPart:
			p.Parts = cloneParts(p.Parts)
			out[i] = p
		case SubstitutionPart:
			p.Statements = cloneStatements(p.Statements)
			out[i] = p
		default:
			out[i] = part
		}
	}
	return out
}
func cloneTest(t TestNode) TestNode {
	switch p := t.(type) {
	case TestWord:
		p.Word = cloneWord(p.Word)
		return p
	case UnaryPredicate:
		p.Operand = cloneTest(p.Operand)
		return p
	case BinaryPredicate:
		p.Left = cloneTest(p.Left)
		p.Right = cloneTest(p.Right)
		return p
	case GroupedPredicate:
		p.Expression = cloneTest(p.Expression)
		return p
	default:
		return nil
	}
}
