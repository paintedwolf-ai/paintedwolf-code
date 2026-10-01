package toolusage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/pkg/api"
)

// Custom workflow fixtures use the same project trust controls as the app.
func (c *liveClient) enableWorkflowFixture(ctx context.Context, projectID, workflowID, workflowVersion string) error {
	body, err := json.Marshal(api.UpdateProjectTrustRequest{Enabled: map[string]bool{
		projectcontrib.SurfaceProjectSettings: true, projectcontrib.SurfacePromptOverrides: true,
	}})
	if err != nil {
		return err
	}
	request, err := c.newRequest(ctx, http.MethodPatch, "/v1/projects/"+projectID+"/trust", bytes.NewReader(body))
	if err != nil {
		return err
	}
	_, err = decodeJSON[api.ProjectTrust](c.do(request)) //nolint:bodyclose // decodeJSON closes the body.
	if err != nil {
		return err
	}
	request, err = c.newRequest(ctx, http.MethodGet, "/v1/workflows?project_id="+projectID, nil)
	if err != nil {
		return err
	}
	available, err := decodeJSON[api.WorkflowListResponse](c.do(request)) //nolint:bodyclose // decodeJSON closes the body.
	if err != nil {
		return err
	}
	for _, candidate := range available.Workflows {
		if candidate.ID == workflowID && candidate.Version == workflowVersion {
			return nil
		}
	}
	return fmt.Errorf("project workflow %s@%s is not available", workflowID, workflowVersion)
}
