package summarize

// Mode records which material inputs were supplied.
type Mode string

const (
	ModeInline Mode = "inline" // content only
	ModeRepo   Mode = "repo"   // path / paths / pattern only
	ModeHybrid Mode = "hybrid" // content plus repo keys
)

// ResolveMode picks the mode from which inputs are present.
func ResolveMode(hasContent, hasRepoKeys bool) Mode {
	switch {
	case hasContent && hasRepoKeys:
		return ModeHybrid
	case hasContent:
		return ModeInline
	default:
		return ModeRepo
	}
}

// GatherStats records observed gather facts.
type GatherStats struct {
	Mode         Mode
	Candidates   int
	PathIsFile   bool
	PathIsDir    bool
	HasPattern   bool
	UseStructure bool
	// NestedReposPruned counts nested VCS roots excluded from walks/greps.
	NestedReposPruned int
	// FaninGrepPasses counts batched fan-in grep passes during gather.
	FaninGrepPasses int
}
