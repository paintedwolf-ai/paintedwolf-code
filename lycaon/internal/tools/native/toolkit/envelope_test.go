package toolkit

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSurveyAnnotationsPreservePayload(t *testing.T) {
	const source = `{"revision":9007199254740993,"nested":{"total":9007199254740995}}`
	withBanner, err := MarshalResponse(json.RawMessage(source), "Page continues")
	testutil.FailErr(t, "attach banner", err)
	withCoverage, err := PatchCoverage(withBanner, 2, 5)
	testutil.FailErr(t, "attach coverage", err)
	var got map[string]json.RawMessage
	testutil.FailErr(t, "decode annotated response", json.Unmarshal([]byte(withCoverage), &got))
	for field, want := range map[string]string{
		"revision":          "9007199254740993",
		"nested":            `{"total":9007199254740995}`,
		"truncation_banner": `"Page continues"`,
		"selected":          "2",
		"total":             "5",
	} {
		if string(got[field]) != want {
			t.Errorf("%s=%s, want %s", field, got[field], want)
		}
	}
}

func TestSurveyAnnotationsRejectNonObjects(t *testing.T) {
	for _, raw := range []string{"null", "[]", `"text"`, "1"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := MarshalResponse(json.RawMessage(raw), "Page continues"); err == nil {
				t.Error("banner accepted a non-object response")
			}
			if _, err := PatchCoverage(raw, 1, 2); err == nil {
				t.Error("coverage accepted a non-object response")
			}
		})
	}
}
