package runstate

var (
	ErrNotFound         = errors.New("workflow run not found")
	ErrRevisionConflict = errors.New("workflow run revision conflict")
)
