package surface

// FlowComparator is a closed when-cell operator.
type FlowComparator int

const (
	FlowCmpEq FlowComparator = iota
	FlowCmpGt0
	FlowCmpPresent
	FlowCmpAbsent
)

// FlowCondition is one ANDed predicate in a rule when block.
type FlowCondition struct {
	Fact       string
	Comparator FlowComparator
	EqBool     bool
	EqString   string
}

// FlowOutput resolves to a coordinator surface id from facts.
type FlowOutput struct {
	SurfaceID  string
	UseFact    string
	DispatchOn string
	Cases      map[string]string
	Default    string
}

// FlowRule is one FIRST-hit table row.
type FlowRule struct {
	When []FlowCondition
	Out  FlowOutput
}

// FlowTable is the loaded coordinator-flow.yaml model.
type FlowTable struct {
	HitPolicy string
	Rules     []FlowRule
	Default   FlowOutput
}

// FlowEvaluation is the result of EvaluateFlow.
type FlowEvaluation struct {
	SurfaceID      string
	BaseModeRef    string
	MatchedRuleIdx int // rule index, or -1 when default matched
}
