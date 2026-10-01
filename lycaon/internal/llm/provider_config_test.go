package llm

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
)

func TestLoadProviderConfigRejectsUnknownFields(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{
		config.Providers: "providers:\n  - id: test\n    knid: test\n",
	})
	if _, err := LoadProviderConfig(); err == nil {
		t.Fatal("unknown provider field was accepted")
	}
}

func TestProviderCatalogRejectsUnknownLocalFields(t *testing.T) {
	stageShipProviders(t, "providers: []\n")
	localPath := filepath.Join(t.TempDir(), "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte("providers:\n  - id: test\n    knid: test\n"))
	if _, err := NewProviderCatalogAt(localPath); err == nil {
		t.Fatal("unknown local provider field was accepted")
	}
}

func TestProviderCatalogRejectsLocalRetryProfileWithoutKindTemplate(t *testing.T) {
	stageShipProviders(t, "providers: []\n")
	localPath := filepath.Join(t.TempDir(), "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte("providers:\n  - id: test\n    http_retry_profile: hosted\n"))
	if _, err := NewProviderCatalogAt(localPath); err == nil {
		t.Fatal("local retry profile selection was accepted")
	}
}
