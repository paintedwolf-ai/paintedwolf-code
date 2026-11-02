package modeladmin

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestEffectivePolicyResolvesInheritedLiteWithoutChangingOverlay(t *testing.T) {
	remote := llm.ModelRef{ProviderID: "remote", Model: "chosen"}
	handler := &Handler{service: &llm.Service{Policy: llm.NewInMemoryPolicyStore(llm.ModelPolicy{Coordinator: remote}), Registry: &llm.Registry{}}}
	for _, effective := range []bool{true, false} {
		path := "/v1/settings/model-policy"
		if effective {
			path += "?effective=true"
		}
		response := httptest.NewRecorder()
		handler.GetPolicy(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 {
			t.Fatalf("response: %s", response.Body.String())
		}
		var policy wire.ModelPolicy
		testutil.FailErr(t, "decode policy", json.Unmarshal(response.Body.Bytes(), &policy))
		if effective && (policy.Lite == nil || policy.Lite.ProviderID != remote.ProviderID) {
			t.Fatalf("effective lite = %+v", policy.Lite)
		}
		if !effective && policy.Lite != nil {
			t.Fatalf("overlay acquired default: %+v", policy.Lite)
		}
	}
}
