package gotreesitter

// protoSourceFileChildrenLookComplete reports whether a set of top-level
// result nodes can legitimately reduce to a proto `source_file` root even
// though one or more children carry parse errors.
//
// In tree-sitter C the proto `source_file` rule is an optional, repeatable
// sequence of top-level statements (every member can be BLANK), so the parser
// always reduces to `source_file` at EOF — error recovery wraps the bad span
// in an ERROR *child* rather than re-tagging the whole root as ERROR. Compare
// the C oracle on `edition = "2023";\n...`:
//
//	(source_file
//	  (ERROR (ERROR) (decimal_lit))
//	  (empty_statement)
//	  (package ...)
//	  (message ...))
//
// Without this allowance Go would synthesize an ERROR root, diverging from C
// (root: go=ERROR c=source_file). Every named proto top-level statement
// (package, message, enum, import, option, service, extend, empty_statement)
// is a valid `source_file` child, and ERROR fragments are the recovery shape
// C itself emits, so we accept a child set composed solely of named nodes and
// error fragments.
func protoSourceFileChildrenLookComplete(nodes []*Node, lang *Language) bool {
	if len(nodes) == 0 || lang == nil || lang.Name != "proto" {
		return false
	}
	for _, n := range nodes {
		if n == nil || n.isExtra() {
			continue
		}
		if n.IsError() || n.HasError() {
			continue
		}
		if !n.IsNamed() {
			return false
		}
	}
	return true
}
