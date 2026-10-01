package survey

// WorkerEnvelope is worker completion metadata for synthesis snapshot assembly.
type WorkerEnvelope struct {
	JobID          string
	ChildSessionID string
	AgentType      string
	Body           string
	Report         WorkerReportSnapshot
}

// WorkerReportSnapshot carries report fields merged into the synthesis snapshot.
type WorkerReportSnapshot struct {
	LegStatus     string
	Brief         string
	ObjectivesMet []string
	RemainingRisk []string
	FilesModified []string
	Findings      []WorkerFindingSnapshot
}

// WorkerFindingSnapshot is one structured finding excerpt for synthesis snapshot rows.
type WorkerFindingSnapshot struct {
	Path         string
	Line         int
	Excerpt      string
	Note         string
	Claim        string
	Adversary    string
	Precondition string
	Severity     string
}
