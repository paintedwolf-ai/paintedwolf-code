package llm

import (
	"context"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	nativegenprovider "github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestNativegenFactoryRequiresHostRuntimeForExecution(t *testing.T) {
	entry := CatalogEntry{ID: "nativegen", Kind: "nativegen", Models: []modelinfo.Entry{nativegenprovider.DefaultModelEntry()}}
	provider, err := driverFactories.Build(context.Background(), entry.Kind, providerBuild{entry: entry})
	if err != nil {
		t.Fatal(err)
	}
	local, ok := provider.(*nativegenprovider.Provider)
	if !ok {
		t.Fatalf("provider = %T", provider)
	}
	if _, err := local.Stream(context.Background(), modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}}}); err == nil {
		t.Fatal("started without a host runtime")
	}
}
