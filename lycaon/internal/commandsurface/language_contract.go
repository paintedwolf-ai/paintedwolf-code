package commandsurface

// LanguageVersion is the syntax contract frozen before runtime implementation.
const LanguageVersion = 1

// SourceSpan identifies source bytes independently of projected output.
type SourceSpan struct {
 StartByte int `json:"start_byte"`
 EndByte int `json:"end_byte"`
 Line int `json:"line"`
 Column int `json:"column"`
}

// ResourceLimit specifies an admission or execution ceiling and its boundary cases.
type ResourceLimit struct {
 ID string `json:"id"`
 Maximum int64 `json:"maximum"`
 Unit string `json:"unit"`
 Scope string `json:"scope"`
 Exceeded string `json:"exceeded"`
 BoundaryFixtures []string `json:"boundary_fixtures"`
}
