package filebriefing

import (
	"time"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func Response(b Briefing) wire.FileBriefingResponse {
	return wire.FileBriefingResponse{
		TargetKey: b.TargetKey, AttemptID: b.AttemptID, RootID: b.RootID, Path: b.Path, Presentation: b.Presentation,
		SourceSHA256: b.SourceSHA256,
		Status:       string(b.Status), Preview: fileBriefingPreviewDTO(b.Preview), Locations: fileBriefingLocationsDTO(b.Locations),
		Sections: fileBriefingSectionsDTO(b.Sections), FallbackText: b.FallbackText,
		Truncated: b.Truncated, Error: b.Error,
		UpdatedAt: b.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}
