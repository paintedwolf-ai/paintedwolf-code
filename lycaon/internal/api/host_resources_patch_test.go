package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPutHostResourceAccessPreservesApprovalSettings(t *testing.T) {
	t.Setenv("LYCAON_HOST_RESOURCE_API_POLICY_TEST", "1")
	catalog := []byte(`version: 1
resources:
  - id: local-db
    family: data-tools
    label: Local database
    category: Data
    description: Local database.
    surfaces: [process_exec]
    realizations:
      - discover:
          environment: {names: [LYCAON_HOST_RESOURCE_API_POLICY_TEST]}
`)
	// The required test services load bundled config before it is replaced.
	deps := requiredTestDeps(t, Dependencies{Core: CoreDependencies{Store: storetest.New(t, testdbseed.DefaultProjectID)}})
	// Both the resource catalog and the approvals floor are bundled config, so
	// they stage together — a second Only call would replace the first.
	configtest.Only(t, map[config.Rel]string{
		config.HostResources: string(catalog),
		config.SecurityApprovals: `never_ask: true
rules:
  - category: tool
    pattern: write
    effect: ask
`,
	})
	approvalStore, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "new approval store", err)
	service, err := hostresources.NewService(t.TempDir())
	testutil.FailErr(t, "new host resource service", err)
	service.SetPolicyBinder(func(_ context.Context, project hostresources.ProjectContext) hostresources.PolicyEvaluator {
		rules := approvalStore.Get(llm.SettingsScopeGlobal, settings.ProjectRef{Dir: project.Dir}).Rules
		return func(id, _ string) hostresources.PolicyDecision {
			effect, _, matched := settings.EvaluateHostResourceSubjects(
				rules, []string{id},
			)
			if !matched {
				return hostresources.PolicyDecision{
					Access: hostresources.AccessAllow, Setting: hostresources.AccessSettingInherit,
				}
			}
			setting := hostresources.AccessSettingInherit
			if exactEffect, exact := settings.ExactHostResourceRule(rules, id); exact {
				setting = hostresources.AccessSetting(exactEffect)
			}
			return hostresources.PolicyDecision{
				Access: hostresources.Access(effect), Setting: setting,
			}
		}
	})
	deps.Core.Settings.Approvals = approvalStore
	deps.Host.HostResources = service
	server := NewServer(deps, nil, TestAPIToken)

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, newAuthedRequest(
		http.MethodPatch, "/v1/host-resources/local-db", bytes.NewBufferString(`{"access":"ask"}`),
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response wire.HostResourcesResponse
	testutil.FailErr(t, "decode response", json.Unmarshal(recorder.Body.Bytes(), &response))
	if len(response.Resources) != 1 || response.Resources[0].Access != wire.HostResourceAccessAsk ||
		response.Resources[0].AccessSetting != wire.HostResourceAccessSettingAsk {
		t.Fatalf("response = %+v", response)
	}
	cfg := approvalStore.Get(llm.SettingsScopeGlobal, settings.ProjectRef{})
	if cfg.NeverAsk == nil || !*cfg.NeverAsk || len(cfg.Rules) != 2 {
		t.Fatalf("lossless approval mutation failed: %+v", cfg)
	}

	// A patch without access merges nothing and answers the current state.
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, newAuthedRequest(
		http.MethodPatch, "/v1/host-resources/local-db", bytes.NewBufferString(`{}`),
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty patch status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	testutil.FailErr(t, "decode empty patch response", json.Unmarshal(recorder.Body.Bytes(), &response))
	if len(response.Resources) != 1 || response.Resources[0].AccessSetting != wire.HostResourceAccessSettingAsk {
		t.Fatalf("empty patch response = %+v", response)
	}
	if after := approvalStore.Get(llm.SettingsScopeGlobal, settings.ProjectRef{}); len(after.Rules) != 2 {
		t.Fatalf("an empty patch changed the approval rules: %+v", after)
	}

	// An access outside the vocabulary is refused naming the field.
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, newAuthedRequest(
		http.MethodPatch, "/v1/host-resources/local-db", bytes.NewBufferString(`{"access":"sometimes"}`),
	))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown access status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
