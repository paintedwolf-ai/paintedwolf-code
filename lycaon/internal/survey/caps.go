package survey

// Synthesis evidence curate thresholds (docs/tools.md § Synthesis evidence curate).
const (
	SynthesisEvidenceBudget      = 24_000
	SynthesisEvidenceMin         = 8_000
	SynthesisCurateBudget        = 15
	SynthesisWorkerReportExcerpt = 1200
	SynthesisTopologyTailBytes   = 4_000
)

// Caps are probe and candidate limits from docs/tools.md § Probe and candidate caps.
type Caps struct {
	MaxProbeBatch      int
	MaxRetainedRecords int
	GrepMatchCap       int
	FindMatchCap       int
	LayoutGroupCap     int
}

// DefaultCaps returns survey limits.
func DefaultCaps() Caps {
	return Caps{
		MaxProbeBatch:      12,
		MaxRetainedRecords: 800,
		GrepMatchCap:       2000,
		FindMatchCap:       2000,
		LayoutGroupCap:     64,
	}
}

func (c Caps) withDefaults() Caps {
	defaults := DefaultCaps()
	if c.MaxProbeBatch <= 0 {
		c.MaxProbeBatch = defaults.MaxProbeBatch
	}
	if c.MaxRetainedRecords <= 0 {
		c.MaxRetainedRecords = defaults.MaxRetainedRecords
	}
	if c.GrepMatchCap <= 0 {
		c.GrepMatchCap = defaults.GrepMatchCap
	}
	if c.FindMatchCap <= 0 {
		c.FindMatchCap = defaults.FindMatchCap
	}
	if c.LayoutGroupCap <= 0 {
		c.LayoutGroupCap = defaults.LayoutGroupCap
	}
	return c
}
