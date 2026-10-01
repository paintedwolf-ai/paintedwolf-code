package contract

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

// stubPtr supplies a present optional wire field.
func stubPtr[T any](value T) *T { return &value }

const (
	fixtureSessionID     = "11111111-1111-4111-8111-111111111111"
	fixtureProjectID     = "22222222-2222-4222-8222-222222222222"
	fixtureBlueprintID   = "33333333-3333-4333-8333-333333333333"
	fixtureDelegationID  = "44444444-4444-4444-8444-444444444444"
	fixtureLegID         = "55555555-5555-4555-8555-555555555555"
	fixtureWorkerID      = "66666666-6666-4666-8666-666666666666"
	fixtureScanID        = "88888888-8888-4888-8888-888888888888"
	fixtureWorkflowRunID = "99999999-9999-4999-8999-999999999999"
	fixtureCheckpointID  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	fixtureFileID        = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	fixtureRootID        = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	fixtureVersionID     = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	fixtureWorkspaceID   = "ws_0123456789abcdef0123456789abcdef"
	fixtureProjectDir    = "/tmp/lycaon-fixture"
	fixtureTime          = "2026-01-01T00:00:00Z"
)

func fixtureSecurityFinding() api.SecurityFinding {
	return api.SecurityFinding{
		RuleID:  "opengrep:lycaon.ruby.sql-string-concat",
		Level:   api.FindingLevelHigh,
		Message: "sql concat",
		Locations: []api.SecurityFindingLocation{{
			URI:       "app.rb",
			StartLine: 10,
		}},
		Fingerprints: api.SecurityFindingFingerprints{Primary: "fixture-fp-01"},
		Tool: api.ToolDescriptor{
			DriverID: "opengrep-sast",
			Name:     "OpenGrep SAST",
		},
		Properties: &api.SecurityFindingProperties{
			Lycaon: &api.SecurityFindingLycaonProperties{
				Kind: api.FindingKindSAST,
			},
		},
	}
}

func fixtureTimeValue() time.Time {
	t, _ := time.Parse(time.RFC3339, fixtureTime)
	return t
}

func stubManagedSecret() api.ManagedSecret {
	return api.ManagedSecret{
		Reference: "{{paintedwolf-secret:bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb}}", Name: "Fixture token",
		Scope: "project", Origin: "generated", Format: "base64url", EntropyBits: 256, CreatedAt: fixtureTime,
		State: "active", Version: 1, UseCount: 0, RevealCount: 0,
	}
}

func stubProviderFeatures(promptCache string) api.ProviderFeatures {
	return api.ProviderFeatures{
		ToolCalls:   true,
		Thinking:    true,
		PromptCache: promptCache,
	}
}

func stubScannerRuntime() api.ScanRuntimePolicy {
	return api.ScanRuntimePolicy{SoftLimitMs: 30000, HardLimitMs: 120000, CPUUnits: 1, Parallelism: 1}
}

func stubProviderModel(id string) api.ProviderModelMeta {
	unknown := api.ProviderCapabilityEvidence{State: "unknown"}
	decision := api.ModelRoleEligibility{State: "unverified", Selectable: false, Code: "chat_unknown", Reason: "Chat support is not listed."}
	return api.ProviderModelMeta{
		Thinking:    api.ThinkingCapabilities{State: "unknown"},
		Eligibility: api.ModelRoleEligibilitySet{Coordinator: decision, AgentPool: decision, Lite: decision},
		ID:          id,
		Capabilities: api.ProviderModelCapabilities{
			Chat: unknown, Streaming: unknown, Tools: unknown, Vision: unknown,
			Reasoning: unknown, StructuredOutput: unknown, PromptCaching: unknown,
		},
	}
}

func stubHostResourcesResponse() api.HostResourcesResponse {
	return api.HostResourcesResponse{
		Version:         1,
		Resources:       []api.HostResource{},
		Diagnostics:     []api.HostResourceDiagnostic{},
		UserCatalogPath: "/tmp/host-resources.yaml",
		CheckedAt:       fixtureTime,
		Fingerprint:     "stub-host-resources",
	}
}

func stubExtensionDesiredState() api.ExtensionDesiredState {
	return api.ExtensionDesiredState{
		Format:   1,
		Packs:    []api.ExtensionDesiredPack{},
		Disabled: []string{},
		Own:      map[string]string{},
	}
}

func stubExtensionsCatalogView() api.ExtensionsCatalogView {
	return api.ExtensionsCatalogView{
		OK:          true,
		Revision:    "stub-revision",
		Packs:       []api.ExtensionPackSummary{},
		MetaPacks:   []api.ExtensionMetaPackSummary{},
		Diagnostics: []api.ExtensionDiagnostic{},
		Units:       []api.ExtensionUnitSummary{},
		Desired:     stubExtensionDesiredState(),
		DesiredPath: "/tmp/extensions.yaml",
	}
}

func stubExtensionMutationResponse() api.ExtensionMutationResponse {
	return api.ExtensionMutationResponse{View: stubExtensionsCatalogView()}
}

func stubWebResearchProvidersResponse() api.WebResearchProvidersResponse {
	cat := stubWebResearchCatalog()
	entries := cat.Entries()
	providers := make([]api.WebResearchProviderMeta, 0, len(entries))
	for _, entry := range entries {
		roles := make([]api.WebResearchProviderRole, 0, len(entry.RolesOrDefault()))
		for _, role := range entry.RolesOrDefault() {
			roles = append(roles, api.WebResearchProviderRole(role))
		}
		slot := entry.CredentialSlot
		if slot == "" {
			slot = entry.OptionalCredentialSlot
		}
		configured := entry.Kind == webresearch.KindKeyless
		providers = append(providers, api.WebResearchProviderMeta{
			ID:                api.WebSearchProvider(entry.ID),
			Kind:              api.WebResearchProviderKind(entry.Kind),
			Label:             entry.Label,
			Roles:             roles,
			DefaultEnabled:    entry.DefaultEnabled,
			Configured:        configured,
			CredentialPresent: false,
			CredentialSlot:    slot,
		})
	}
	return api.WebResearchProvidersResponse{
		Direct: api.WebResearchDirectStatus{
			Configured: false,
			Card: api.WebResearchDirectCardContent{
				ProviderID: "direct",
				Kind:       "direct",
				Label:      "Direct search",
			},
		},
		Providers: providers,
	}
}

func stubWebResearchSettings() api.WebResearchSettings {
	cat := stubWebResearchCatalog()
	s := webresearch.DefaultSettings(nil, nil, cat)
	return api.WebResearchSettings{
		Warming:          true,
		GuessDomains:     true,
		SearchEnabled:    s.SearchEnabled,
		EnabledProviders: s.EnabledProviders,
	}
}

var (
	stubCatalogOnce sync.Once
	stubCatalog     *webresearch.Catalog
	stubCatalogErr  error
)

func stubWebResearchCatalog() *webresearch.Catalog {
	stubCatalogOnce.Do(func() {
		// The fixture reads the embedded catalog.
		stubCatalog, stubCatalogErr = webresearch.LoadCatalog()
	})
	if stubCatalogErr != nil {
		panic("stub web research catalog: " + stubCatalogErr.Error())
	}
	return stubCatalog
}

type stubJSONWriter func(http.ResponseWriter, int, any)

func NewStubServer() *httptest.Server {
	mux := http.NewServeMux()
	now := fixtureTimeValue()

	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(v); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}

	registerStubWorkspaceRoutes(mux, now, writeJSON)
	registerStubExecutionRoutes(mux, now, writeJSON)
	registerStubSettingsWorkflowRoutes(mux, now, writeJSON)

	return httptest.NewServer(mux)
}
