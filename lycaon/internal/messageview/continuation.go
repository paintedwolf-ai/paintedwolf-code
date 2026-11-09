package messageview

// ContinuationFormat identifies a rebuildable history projection.
const ContinuationFormat = 2

// MaxCompactionRevisionLag bounds source validation before a projection is rebuilt.
const MaxCompactionRevisionLag int64 = 4096

// MaxCompactionRows bounds persisted projections independently of transcript length.
const MaxCompactionRows = 4096

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
	MessageID   string                 `json:"message_id"`
	Ord         int64                  `json:"ord"`
	Tool        string                 `json:"tool,omitempty"`
	Handles     []string               `json:"handles,omitempty"`
	Disposition string                 `json:"disposition"`
	Tokens      int                    `json:"tokens"`
	Execution   *ContinuationExecution `json:"execution,omitempty"`
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
		if record.Sources[i].Execution != nil {
			execution := *record.Sources[i].Execution
			if execution.ExitCode != nil {
				code := *execution.ExitCode
				execution.ExitCode = &code
			}
			copy.Sources[i].Execution = &execution
		}
	}
	return &copy
}

// ContinuationExecution preserves recorded outcomes independently of summary prose.
type ContinuationExecution struct {
	CallID         string `json:"call_id"`
	ReceiptID      string `json:"receipt_id,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
	Status         string `json:"status,omitempty"`
	Invoked        bool   `json:"invoked"`
	SourceRevision string `json:"source_revision,omitempty"`
	SourceVerdict  string `json:"source_verdict,omitempty"`
	ProcessHandle  string `json:"process_handle,omitempty"`
	ProgramID      string `json:"program_id,omitempty"`
	ProgramState   string `json:"program_state,omitempty"`
	ExitCode       *int   `json:"exit_code,omitempty"`
	StopCode       string `json:"stop_code,omitempty"`
}
