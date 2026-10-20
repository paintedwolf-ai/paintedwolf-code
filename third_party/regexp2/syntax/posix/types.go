package syntax

import standard "regexp/syntax"

type Regexp = standard.Regexp
type Op = standard.Op

const (
	OpNoMatch        = standard.OpNoMatch
	OpEmptyMatch     = standard.OpEmptyMatch
	OpLiteral        = standard.OpLiteral
	OpCharClass      = standard.OpCharClass
	OpAnyCharNotNL   = standard.OpAnyCharNotNL
	OpAnyChar        = standard.OpAnyChar
	OpBeginLine      = standard.OpBeginLine
	OpEndLine        = standard.OpEndLine
	OpBeginText      = standard.OpBeginText
	OpEndText        = standard.OpEndText
	OpWordBoundary   = standard.OpWordBoundary
	OpNoWordBoundary = standard.OpNoWordBoundary
	OpCapture        = standard.OpCapture
	OpStar           = standard.OpStar
	OpPlus           = standard.OpPlus
	OpQuest          = standard.OpQuest
	OpRepeat         = standard.OpRepeat
	OpConcat         = standard.OpConcat
	OpAlternate      = standard.OpAlternate
	opPseudo         = 128
)
