package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerretry"
	nativegenprovider "github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/modelstore"
)

type testManifestSigner struct{}

func (testManifestSigner) Verify(modelstore.SignedManifest) error { return nil }

func TestNativegenProviderFactoryResolvesActivatedModelStoreRevision(t *testing.T) {
	storeRoot := t.TempDir()
	t.Setenv("LYCAON_MODEL_STORE_DIR", storeRoot)

	store := modelstore.New(storeRoot, testManifestSigner{})
	payload := []byte("weights")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	art := modelstore.Artifact{
		Name:   "model.safetensors",
		Digest: digest,
		Format: "safetensors",
		Bytes:  int64(len(payload)),
	}
	if err := store.Stage(bytes.NewReader(payload), art); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	manifest := modelstore.Manifest{
		Model:    nativegenprovider.DefaultModelID,
		Revision: "rev-factory",
		Device:   "cpu",
		Artifact: art,
	}
	if err := store.Activate(modelstore.SignedManifest{Manifest: manifest, Signature: "sig"}); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	entry := CatalogEntry{
		ID:      "nativegen",
		Kind:    "nativegen",
		BaseURL: "/usr/local/bin/nativegen",
		Models:  []ModelEntry{nativegenprovider.DefaultModelEntry()},
	}
	prov, err := newProviderForEntry(context.Background(), entry, "", providerretry.ProviderHTTPRetry{}, nil, nil)
	if err != nil {
		t.Fatalf("newProviderForEntry: %v", err)
	}
	ng, ok := prov.(*nativegenprovider.Provider)
	if !ok {
		t.Fatalf("expected *nativegen.Provider, got %T", prov)
	}
	defer ng.Close()

	if got := ng.ModelDir(); got == "" {
		t.Fatal("expected factory-constructed nativegen provider to resolve ModelDir from active modelstore revision")
	}
}
