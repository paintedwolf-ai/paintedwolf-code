package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/hostpower"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPowerSettingsDefaultOnAndApplyImmediately(t *testing.T) {
	controller := hostpower.New(true)
	defer func() { _ = controller.Close() }()
	srv, _, _ := contractfixture.NewSettingsTestServer(t, func(d *hostapi.Dependencies) { d.Host.HostPower = controller })

	get := httptest.NewRecorder()
	srv.ServeHTTP(get, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/settings/power", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", get.Code, get.Body.String())
	}
	var initial wire.PowerSettingsResponse
	testutil.FailErr(t, "decode initial power settings", json.Unmarshal(get.Body.Bytes(), &initial))
	if !initial.KeepAwakeWhileWorking {
		t.Fatal("power setting should default on")
	}

	Put := httptest.NewRecorder()
	req := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/settings/power", strings.NewReader(`{"keep_awake_while_working":false}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(Put, req)
	if Put.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d body=%s", Put.Code, Put.Body.String())
	}
	if controller.Snapshot().Enabled || srv.Admin.Project.Trust.Settings.Power.KeepAwakeWhileWorking() {
		t.Fatal("disabled preference was not applied to persistence and runtime")
	}
}
