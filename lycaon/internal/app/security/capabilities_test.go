package security

import (
	"bytes"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

type capabilityAuthority struct {
	service *secretcap.Service
	calls   int
}

func (a *capabilityAuthority) SetSecretResolver(service *secretcap.Service) {
	a.service = service
	a.calls++
}

func TestCapabilityBootstrapRegistersScreenedDurableSecretsOnce(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	database := testdbfixture.Open(t, "capabilities.db")
	testdbseed.InsertSession(t, database, "root-1", testdbseed.DefaultProjectID)
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x31}, 32))
	testutil.FailErr(t, "create secret fingerprinter", err)
	runtime := New(t.Context(), database, nil, nil, nil)
	runtime.Fingerprinter, runtime.Harvest = fp, secretharvest.NewRuntime(fp)
	registry, authority := tools.NewDefaultRegistry(), &capabilityAuthority{}
	testutil.FailErr(t, "bootstrap secret capabilities", runtime.BuildCapabilities(registry, authority))
	for _, name := range []string{native.SecretGenerateTool, native.SecretListTool, native.SecretRevokeTool} {
		if _, ok := registry.Definition(name); !ok {
			t.Fatalf("capability tool %s was not registered", name)
		}
	}
	metadata, err := authority.service.Generate(t.Context(), secretcap.GenerateRequest{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1", OperationID: "generate-1", Name: "registry token", Purpose: "authenticate registry"})
	testutil.FailErr(t, "generate bootstrapped secret", err)
	protected := runtime.Harvest.ValuesFor("root-1")
	if len(protected) != 1 || protected[0].Secret() == "" {
		t.Fatalf("generated secret was not screened: %d values", len(protected))
	}
	listed, err := authority.service.List(t.Context(), testdbseed.DefaultProjectID, "root-1")
	testutil.FailErr(t, "read durable secret metadata", err)
	if len(listed) != 1 || listed[0].Reference != metadata.Reference {
		t.Fatalf("generated reference missing: %+v", listed)
	}
	service := authority.service
	testutil.FailErr(t, "repeat capability bootstrap", runtime.BuildCapabilities(registry, authority))
	if authority.calls != 1 || runtime.Capabilities != service {
		t.Fatal("repeat bootstrap replaced the live secret resolver")
	}
}
