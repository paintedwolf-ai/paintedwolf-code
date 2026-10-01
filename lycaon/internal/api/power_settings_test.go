package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostpower"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPowerSettingsDefaultOnAndApplyImmediately(t *testing.T) {
	controller := hostpower.New(true)
	defer func() { _ = controller.Close() }()
	srv, _, _ := newSettingsTestServer(t, func(d *Dependencies) { d.HostPower = controller })

	get := httptest.NewRecorder()
	srv.ServeHTTP(get, newAuthedRequest(http.MethodGet, "/v1/settings/power", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", get.Code, get.Body.String())
	}
	var initial wire.PowerSettingsResponse
	testutil.FailErr(t, "decode initial power settings", json.Unmarshal(get.Body.Bytes(), &initial))
	if !initial.KeepAwakeWhileWorking {
		t.Fatal("power setting should default on")
	}

	put := httptest.NewRecorder()
	req := newAuthedRequest(http.MethodPatch, "/v1/settings/power", strings.NewReader(`{"keep_awake_while_working":false}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(put, req)
	if put.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d body=%s", put.Code, put.Body.String())
	}
	if controller.Snapshot().Enabled || srv.settingsSvc.Power.KeepAwakeWhileWorking() {
		t.Fatal("disabled preference was not applied to persistence and runtime")
	}
}
