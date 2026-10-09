package sizebudget

// TrackingReport is a complete inventory of artifacts above the cleanup threshold.
// Admission findings remain change-scoped; this inventory reconciles main's debt.
type TrackingReport struct {
	SchemaVersion int                `json:"schema_version"`
	Complete      bool               `json:"complete"`
	Artifacts     []TrackingArtifact `json:"artifacts"`
}

// TrackingArtifact carries the measurements needed to maintain one cleanup issue.
type TrackingArtifact struct {
	Category        string         `json:"category"`
	ID              string         `json:"id"`
	Touched         bool           `json:"touched"`
	Measured        int            `json:"measured"`
	Warn            int            `json:"warn"`
	Limit           int            `json:"limit"`
	EffectiveCap    int            `json:"effective_cap"`
	ExceptionReason string         `json:"exception_reason"`
	Spans           []TrackingSpan `json:"spans"`
	Sources         []string       `json:"sources"`
}

// TrackingSpan locates a Go declaration or receiver method in the measured head.
type TrackingSpan struct {
	File  string `json:"file"`
	First int    `json:"first"`
	Last  int    `json:"last"`
}
