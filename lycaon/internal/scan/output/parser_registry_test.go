package output_test

import (
	"testing"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
)

func TestRegisteredOutputParserIDsComplete(t *testing.T) {
	for _, id := range []string{
		scanoutput.OutputParserSARIF,
		scanoutput.OutputParserOpengrepJSON,
		scanoutput.OutputParserFindingsJSON,
		scanoutput.OutputParserMapJSON,
	} {
		if !scanoutput.IsRegisteredOutputParser(id) {
			t.Fatalf("parser %q not registered", id)
		}
	}
}
