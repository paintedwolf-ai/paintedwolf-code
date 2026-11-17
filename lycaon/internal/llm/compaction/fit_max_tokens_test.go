package compaction

import (
	"testing"
)

func TestResolveColdStartOverhead(t *testing.T) {
	if got := ResolveColdStartOverhead(""); got != defaultColdStartFitOverheadTokens {
		t.Fatalf("empty tools = %d want %d", got, defaultColdStartFitOverheadTokens)
	}
	hugeTools := string(make([]byte, defaultColdStartFitOverheadTokens*4+400))
	if got := ResolveColdStartOverhead(hugeTools); got <= defaultColdStartFitOverheadTokens {
		t.Fatalf("toolsJSON cold-start = %d want > %d", got, defaultColdStartFitOverheadTokens)
	}
}
