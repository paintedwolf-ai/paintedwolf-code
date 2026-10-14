package messageview

// ContinuationFormat identifies a rebuildable history projection.
const ContinuationFormat = 1

// MaxCompactionRevisionLag bounds source validation before a projection is rebuilt.
const MaxCompactionRevisionLag int64 = 4096

// Continuation covers canonical observations independently of summary quality.
type Continuation struct {
	Level             string               `json:"level"`
	Format            int                  `json:"format"`
	SummaryThroughOrd int64                `json:"summary_through_ord"`
	ThroughOrd        int64                `json:"through_ord"`
	RemovedRows       int                  `json:"removed_rows"`
	Summary           ContinuationSummary  `json:"summary"`
	Sources           []ContinuationSource `json:"sources"`
	Receiver          string               `json:"receiver"`
	Window            int                  `json:"window"`
	Reason            string               `json:"reason"`
}

// ContinuationSummary contains model-authored navigation clues, not host state.
type ContinuationSummary struct {
	Facts       []string `json:"facts"`
	Completed   []string `json:"completed"`
	Pending     []string `json:"pending"`
	Reacquire   []string `json:"reacquire"`
	Constraints []string `json:"constraints"`
}

// ContinuationSource records the disposition of one removed observation.
type ContinuationSource struct {
	MessageID   string   `json:"message_id"`
	Ord         int64    `json:"ord"`
	Tool        string   `json:"tool,omitempty"`
	Handles     []string `json:"handles,omitempty"`
	Disposition string   `json:"disposition"`
	Tokens      int      `json:"tokens"`
}

// CloneContinuation separates an accepted projection from mutable consumers.
func CloneContinuation(record *Continuation) *Continuation {
	if record == nil {
		return nil
	}
	copy := *record
	copy.Summary.Facts = append([]string(nil), record.Summary.Facts...)
	copy.Summary.Completed = append([]string(nil), record.Summary.Completed...)
	copy.Summary.Pending = append([]string(nil), record.Summary.Pending...)
	copy.Summary.Reacquire = append([]string(nil), record.Summary.Reacquire...)
	copy.Summary.Constraints = append([]string(nil), record.Summary.Constraints...)
	copy.Sources = append([]ContinuationSource(nil), record.Sources...)
	for i := range copy.Sources {
		copy.Sources[i].Handles = append([]string(nil), record.Sources[i].Handles...)
	}
	return &copy
}
