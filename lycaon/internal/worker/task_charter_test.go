package worker

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSharedContextPreservesContractAndHasIndependentByteBudget(t *testing.T) {
	contract := "type Item = {\n  value: string;\n}\n\nrender(item);"
	args := map[string]any{"brief": map[string]any{"goal": "integrate", "done_when": []string{"shared interface works"}}, "shared_context": contract}
	charter, err := parseTaskCharter(args)
	testutil.FailErr(t, "parse shared contract", err)
	if charter.SharedContext != contract || !strings.Contains(formatTaskCharter(charter), contract) {
		t.Fatal("shared contract formatting changed")
	}
	args["shared_context"] = strings.Repeat("a", 8192)
	_, err = parseTaskCharter(args)
	testutil.FailErr(t, "accept independent context allowance", err)
	args["shared_context"] = strings.Repeat("界", 2731)
	if _, err := parseTaskCharter(args); err == nil {
		t.Fatal("accepted context exceeding byte budget")
	}
}
