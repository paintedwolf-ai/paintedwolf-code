package llm

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryUnwiredScreenRefusesEveryRequest(t *testing.T) {
	inner := &responseRetryStub{}
	registry := &Registry{}
	registry.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: map[string]modelcall.Provider{inner.ID(): inner}, DefaultID: inner.ID(),
	}))
	selected, err := registry.Get(inner.ID())
	testutil.FailErr(t, "get provider", err)
	for _, provider := range []modelcall.Provider{selected, registry.Default()} {
		if provider.ID() != inner.ID() || !reflect.DeepEqual(provider.Models(), inner.Models()) || !reflect.DeepEqual(provider.Profile(), inner.Profile()) {
			t.Fatal("unwired screen changed catalog metadata")
		}
		_, completeErr := provider.Complete(t.Context(), modelcall.CompletionRequest{})
		_, streamErr := provider.Stream(t.Context(), modelcall.CompletionRequest{})
		for _, err := range []error{completeErr, streamErr} {
			var fault *ModelRequestSecretScreenFaultError
			if !errors.As(err, &fault) || fault.Stage != secretmatch.FaultStageScreenUnwired || !SecretScreenBlocked(err) {
				t.Fatalf("request error = %v, want unwired screen fault", err)
			}
		}
	}
	if inner.completeCalls != 0 || inner.streamCalls != 0 {
		t.Fatalf("unscreened dispatch: complete=%d stream=%d", inner.completeCalls, inner.streamCalls)
	}
	registry.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	provider, err := registry.Get(inner.ID())
	testutil.FailErr(t, "get explicitly screened provider", err)
	if _, ok := provider.(*lifecycleProvider); !ok {
		t.Fatal("screen installation lost lifecycle wrapper")
	}
}
