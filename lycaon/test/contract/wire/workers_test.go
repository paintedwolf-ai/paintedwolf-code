package contract

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestContractWorkers(t *testing.T) {
	t.Parallel()
	srv := NewStubServer()
	t.Cleanup(srv.Close)
	client := srv.Client()
	base := srv.URL

	q := fmt.Sprintf("?project_id=%s", fixtureProjectID)
	var workers api.WorkerListResponse
	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/workers"+q, nil, http.StatusOK, &workers)

	var worker api.WorkerTask
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/workers/"+fixtureWorkerID+"/cancel", nil, http.StatusAccepted, &worker)
}
