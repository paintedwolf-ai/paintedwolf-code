package sizebudget

// ArtifactReview records a design decision without changing admission limits.
type ArtifactReview struct {
	Kind           string            `json:"kind"`
	Workstream     string            `json:"workstream"`
	Disposition    string            `json:"disposition"`
	Reason         string            `json:"reason"`
	ReviewedCommit string            `json:"reviewed_commit"`
	Ceiling        int               `json:"ceiling"`
	Boundaries     map[string]string `json:"boundaries"`
	State          string            `json:"state"`
}
