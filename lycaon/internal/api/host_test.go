package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/version"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func hostRequest(remoteAddr string) *http.Request {
	req := newAuthedRequest(http.MethodGet, "/v1/host", nil)
	req.RemoteAddr = remoteAddr
	return req
}

func TestHostIdentifiesHostContractAndCaller(t *testing.T) {
	identity, err := hostidentity.LoadOrCreate(t.TempDir())
	testutil.FailErr(t, "create host identity", err)
	srv := newTestServer(t, func(d *Dependencies) { d.HostIdentity = identity })
	owner, err := srv.sessionStore.HostOwner(t.Context())
	testutil.FailErr(t, "read host owner", err)

	res := httptest.NewRecorder()
	srv.ServeHTTP(res, hostRequest("127.0.0.1:52100"))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body)
	}
	var got wire.HostInfo
	testutil.FailErr(t, "decode host info", json.Unmarshal(res.Body.Bytes(), &got))
	if got.HostID != identity.HostID || got.HostPublicKey != identity.EncodedPublicKey() {
		t.Fatalf("host identity = %s/%s, want %s", got.HostID, got.HostPublicKey, identity.HostID)
	}
	if got.ContractVersion != contractVersion || got.ProductVersion != version.Version {
		t.Fatalf("versions = contract %q product %q", got.ContractVersion, got.ProductVersion)
	}
	if got.Caller != owner.Wire() {
		t.Fatalf("caller = %+v, want host owner %+v", got.Caller, owner.Wire())
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0] != wire.HostCapabilitySharedDevice {
		t.Fatalf("loopback capabilities = %v", got.Capabilities)
	}
}

func TestHostOffersSharedDeviceOnlyToLoopbackPeers(t *testing.T) {
	identity, err := hostidentity.LoadOrCreate(t.TempDir())
	testutil.FailErr(t, "create host identity", err)
	srv := newTestServer(t, func(d *Dependencies) { d.HostIdentity = identity })
	for remote, shared := range map[string]bool{
		"127.0.0.1:1":       true,
		"[::1]:1":           true,
		"192.168.1.20:1":    false,
		"[fe80::1]:1":       false,
		"not-an-address":    false,
		"localhost.test:80": false,
	} {
		res := httptest.NewRecorder()
		srv.ServeHTTP(res, hostRequest(remote))
		var got wire.HostInfo
		testutil.FailErr(t, "decode host info for "+remote, json.Unmarshal(res.Body.Bytes(), &got))
		if (len(got.Capabilities) == 1) != shared {
			t.Fatalf("%s capabilities = %v, want shared=%v", remote, got.Capabilities, shared)
		}
	}
}

func TestOperationAuthorizationRequiresACallerWithAuthority(t *testing.T) {
	srv := newTestServer(t)
	reached := false
	handler := srv.authorizeOperation(operationGetHost, func(http.ResponseWriter, *http.Request) { reached = true })
	for name, caller := range map[string]*people.Person{
		"no caller":    nil,
		"unknown role": {ID: "person-1", Role: wire.PersonRole("unrecognized")},
	} {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "/v1/host", nil)
		if caller != nil {
			req = req.WithContext(people.WithCaller(req.Context(), *caller))
		}
		res := httptest.NewRecorder()
		handler(res, req)
		if reached || res.Code != http.StatusForbidden {
			t.Fatalf("%s: reached=%v status=%d, want 403", name, reached, res.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/host", nil)
	req = req.WithContext(people.WithCaller(req.Context(), testutil.HostOwner()))
	handler(httptest.NewRecorder(), req)
	if !reached {
		t.Fatal("owner was not admitted")
	}
}

func TestAuthenticatedRequestsActForTheHostOwner(t *testing.T) {
	srv := newTestServer(t)
	owner, err := srv.sessionStore.HostOwner(t.Context())
	testutil.FailErr(t, "read host owner", err)
	var bound people.Person
	handler := srv.bindCaller(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		bound = requestscope.Caller(r)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), newAuthedRequest(http.MethodGet, "/v1/host", nil))
	if bound != owner {
		t.Fatalf("bound caller = %+v, want host owner %+v", bound, owner)
	}
}
