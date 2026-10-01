package security

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestWebResearchCredentialRoundtrip(t *testing.T) {
	h := wiring.BuildForTest(t)
	configDir, err := configdir.UserConfigDir()
	testutil.FailErr(t, "UserConfigDir", err)
	credPath := filepath.Join(configDir, "credential-vault.age")
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	getStatus := func() wire.WebResearchProvidersResponse {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/v1/web-research/providers", nil)
		testutil.FailErr(t, "NewRequest", err)
		req.Header.Set("Authorization", "Bearer "+h.APIToken)
		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "Do", err)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET status = %d", resp.StatusCode)
		}
		var body wire.WebResearchProvidersResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			testutil.FailErr(t, "Decode", err)
		}
		return body
	}

	credentialStored := func(status wire.WebResearchProvidersResponse, slot string) bool {
		for _, p := range status.Providers {
			if p.CredentialSlot == slot {
				return p.CredentialPresent
			}
		}
		return false
	}

	st := getStatus()
	if credentialStored(st, "brave-search") || credentialStored(st, "marginalia") {
		t.Fatalf("initial status = %+v want both false", st.Providers)
	}

	put := func(id, key string) {
		t.Helper()
		payload, mErr := json.Marshal(map[string]string{"api_key": key})
		testutil.FailErr(t, "marshal credentials", mErr)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, base+"/v1/web-research/providers/"+id+"/credential", bytes.NewReader(payload))
		testutil.FailErr(t, "NewRequest", err)
		req.Header.Set("Authorization", "Bearer "+h.APIToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "Do", err)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT %s status = %d", id, resp.StatusCode)
		}
	}

	// Credentials are addressed by provider; brave reads the brave-search slot.
	put("brave", "brave-test-key")
	if !credentialStored(getStatus(), "brave-search") {
		t.Fatal("expected brave configured")
	}

	data, err := os.ReadFile(credPath)
	testutil.FailErr(t, "ReadFile", err)
	if bytes.Contains(data, []byte("brave-test-key")) {
		t.Fatal("credential vault must encrypt the API key at rest")
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/v1/web-research/providers", nil)
	testutil.FailErr(t, "NewRequest", err)
	req.Header.Set("Authorization", "Bearer "+h.APIToken)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "Do", err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	testutil.FailErr(t, "ReadAll", err)
	if bytes.Contains(body, []byte("brave-test-key")) {
		t.Fatalf("GET must not return raw api key: %s", string(body))
	}

	del := func(id string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, base+"/v1/web-research/providers/"+id+"/credential", nil)
		testutil.FailErr(t, "NewRequest", err)
		req.Header.Set("Authorization", "Bearer "+h.APIToken)
		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "Do", err)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("DELETE %s status = %d", id, resp.StatusCode)
		}
	}

	del("brave")
	if credentialStored(getStatus(), "brave-search") {
		t.Fatal("expected brave cleared")
	}
}
