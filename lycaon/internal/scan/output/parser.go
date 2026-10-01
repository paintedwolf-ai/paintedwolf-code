package output

// Well-known output parser IDs referenced by scanners.yaml.
const (
	OutputParserSARIF        = "sarif"
	OutputParserOpengrepJSON = "opengrep_json"
	// OutputParserFindingsJSON is the BYOK-facing findings envelope (format 1).
	OutputParserFindingsJSON = "lycaon_findings_json"
	// OutputParserMapJSON maps arbitrary JSON via a device mapper file.
	OutputParserMapJSON = "map/json"
)

// IsUserExternalParserAllowed reports whether user scanners may declare parserID.
func IsUserExternalParserAllowed(parserID string) bool {
	switch parserID {
	case OutputParserSARIF, OutputParserFindingsJSON, OutputParserMapJSON:
		return true
	default:
		return false
	}
}

// OutputParser normalizes tool stdout into a Result.
type OutputParser interface {
	ID() string
	Parse(raw []byte) (*Result, error)
}
