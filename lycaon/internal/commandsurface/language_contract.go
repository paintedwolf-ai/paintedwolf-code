package commandsurface

// LanguageVersion is the syntax contract frozen before runtime implementation.
const LanguageVersion = 1

// SourceSpan uses zero-based UTF-8 byte offsets with an exclusive end; line and
// UTF-8 byte column are one-based. The containing unit supplies source identity.
type SourceSpan struct {
	StartByte int `json:"start_byte"`
	EndByte   int `json:"end_byte"`
	Line      int `json:"line"`
	Column    int `json:"column"`
}

// ResourceLimit specifies an admission or execution ceiling and its boundary cases.
type ResourceLimit struct {
	ID               string   `json:"id"`
	Maximum          int64    `json:"maximum"`
	Unit             string   `json:"unit"`
	Scope            string   `json:"scope"`
	Exceeded         string   `json:"exceeded"`
	Enforcement      string   `json:"enforcement"`
	BoundaryModel    string   `json:"boundary_model"`
	Overflow         string   `json:"overflow"`
	BoundaryFixtures []string `json:"boundary_fixtures"`
}
