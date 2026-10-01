package contract

import (
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestContractDelegations(t *testing.T) {
	t.Parallel()
	srv := NewStubServer()
	t.Cleanup(srv.Close)
	client := srv.Client()
	base := srv.URL

	var delegation api.Delegation
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/delegations", api.CreateDelegationRequest{
		ProjectID: fixtureProjectID,
		Task:      "implement feature",
		Strategy:  api.HuntStrategyFileBased,
	}, http.StatusCreated, &delegation)
	contractcheck.AssertJSONRoundTrip(t, &delegation)

	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/delegations/"+fixtureDelegationID, nil, http.StatusOK, &delegation)

	var legs api.DelegationLegListResponse
	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/delegations/"+fixtureDelegationID+"/legs", nil, http.StatusOK, &legs)

	var leg api.Leg
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/delegations/"+fixtureDelegationID+"/dispatch", api.DispatchRequest{
		LegID: fixtureLegID,
	}, http.StatusOK, &leg)

	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/delegations/"+fixtureDelegationID+"/abort", api.AbortDelegationRequest{
		Reason: "done",
	}, http.StatusOK, &delegation)
}
