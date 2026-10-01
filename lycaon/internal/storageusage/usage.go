// Package storageusage reports retained content by storage lane.
package storageusage

// Lane names one retained content store.
type Lane string

const (
	LaneArtifacts         Lane = "artifacts"
	LaneSourceBlobs       Lane = "source_blobs"
	LanePromptAttachments Lane = "prompt_attachments"
)

// Scope names the boundary covered by a measurement.
type Scope string

const (
	ScopeProject Scope = "project"
	ScopeDevice  Scope = "device"
)

// Usage reports one lane's retained bytes.
type Usage struct {
	Lane      Lane  `json:"lane"`
	Scope     Scope `json:"scope"`
	UsedBytes int64 `json:"used_bytes"`
}

// Report groups storage lanes without mixing their scopes.
type Report struct {
	Lanes []Usage `json:"lanes"`
}

// NewReport preserves lane order.
func NewReport(lanes ...Usage) Report {
	return Report{Lanes: append([]Usage(nil), lanes...)}
}
