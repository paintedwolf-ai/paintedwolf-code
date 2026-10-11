package policyfacts

import (
	"bytes"
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func newWeakSecretMintObserveManager(t *testing.T) (*Service, *secretharvest.Runtime) {
	t.Helper()
	ins, err := secretmint.LoadBundled()
	testutil.FailErr(t, "LoadBundled", err)
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x11}, 32))
	testutil.FailErr(t, "NewFingerprinter", err)
	harvest := secretharvest.NewRuntime(fp)
	mgr := New(nil)
	mgr.SetCredentialSlotProvider(func(context.Context, *api.Session) *secretmint.Inspector { return ins })
	mgr.SetSecretFingerprinter(fp)
	mgr.SetHarvestedFingerprint(func(root string, fp secretmatch.SecretFingerprint) bool {
		return harvest.Has(root, fp)
	})
	return mgr, harvest
}

func TestCredentialCandidateObservationDoesNotLatchOrFilter(t *testing.T) {
	mgr, harvest := newWeakSecretMintObserveManager(t)
	sess := &api.Session{ID: "credential-observation"}
	harvest.Harvest(secretharvest.ContainerRead{RootSessionID: sess.ID, Container: ".env", Content: []byte("MYSQL_PASSWORD=password\n")})
	candidates := mgr.credentialSlots(t.Context(), sess).Inspect("write", map[string]any{"content": "MYSQL_PASSWORD=password\n"})
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v", candidates)
	}
	for range 2 {
		gc := mgr.candidateContext(context.Background(), sess, "write", nil, candidates[0])
		for _, name := range []string{"paintedwolf.credential_harvested", "paintedwolf.credential_length", "paintedwolf.credential_fingerprint"} {
			testutil.FailErr(t, "produce "+name, gc.Ensure(name))
		}
		if gc.Published["paintedwolf.credential_harvested"] != true || gc.Published["paintedwolf.credential_length"] != 8 {
			t.Fatalf("facts = %#v", gc.Published)
		}
		if gc.Published["paintedwolf.credential_fingerprint"] == "password" {
			t.Fatal("plaintext fingerprint")
		}
	}
}

func TestCredentialFingerprintMissingIsAProviderFailure(t *testing.T) {
	mgr, _ := newWeakSecretMintObserveManager(t)
	mgr.secretFP = nil
	gc := mgr.candidateContext(t.Context(), &api.Session{ID: "missing"}, "write", nil, secretmint.Candidate{Value: "password"})
	if err := gc.Ensure("paintedwolf.credential_fingerprint"); err == nil {
		t.Fatal("missing fingerprinter did not fail")
	}
}
