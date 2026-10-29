package commandsurface

// ExecutionLocations lists every static location an execution can begin at,
// once each: statements (nested and inside substitutions), substitutions and
// redirections.
func ExecutionLocations(statements []Statement) []SyntaxLocation {
	var out []SyntaxLocation
	var statement func(Statement)
	var words func([]DeferredWord)
	var parts func([]WordNode)
	list := func(items []Statement) {
		for _, item := range items {
			statement(item)
		}
	}
	parts = func(nodes []WordNode) {
		for _, node := range nodes {
			switch node := node.(type) {
			case DoubleQuotedPart:
				parts(node.Parts)
			case SubstitutionPart:
				out = append(out, node.SyntaxLocation)
				list(node.Statements)
			case ParameterPart:
				words(node.Words())
			case ArithmeticPart:
				words(node.Words())
			case BracePart:
				words(node.Alternatives)
			}
		}
	}
	words = func(items []DeferredWord) {
		for _, item := range items {
			parts(item.Parts)
		}
	}
	statement = func(s Statement) {
		out = append(out, s.SyntaxLocation)
		for _, redirect := range s.Redirects {
			out = append(out, redirect.SyntaxLocation)
			words([]DeferredWord{redirect.Target})
		}
		switch node := s.Command.(type) {
		case SimpleCommand:
			words(node.Words)
			for _, assignment := range node.Assignments {
				if assignment.Index != nil {
					words(ArithmeticPart{Expression: assignment.Index}.Words())
				}
				words([]DeferredWord{assignment.Value})
				words(assignment.Array)
			}
		case EnvironmentDeclaration:
			words(node.Words)
			for _, assignment := range node.Assignments {
				words([]DeferredWord{assignment.Value})
			}
		case BinaryCommand:
			statement(node.Left)
			statement(node.Right)
		case StatementGroup:
			list(node.Statements)
		case Conditional:
			for branch := &node; branch != nil; branch = branch.Else {
				list(branch.Condition)
				list(branch.Then)
			}
		case WhileLoop:
			list(node.Condition)
			list(node.Body)
		case ForLoop:
			words(node.Items)
			list(node.Body)
		case CaseStatement:
			words([]DeferredWord{node.Word})
			for _, item := range node.Items {
				words(item.Patterns)
				list(item.Body)
			}
		case FunctionDeclaration:
			statement(node.Body)
		case ArithmeticCommand:
			words(node.Words())
		case TrapCommand:
			list(node.Statements)
		case TimedCommand:
			list(node.Pipeline)
		}
	}
	list(statements)
	seen := map[NodeID]bool{}
	unique := out[:0]
	for _, location := range out {
		if !seen[location.ID] {
			seen[location.ID] = true
			unique = append(unique, location)
		}
	}
	return unique
}
