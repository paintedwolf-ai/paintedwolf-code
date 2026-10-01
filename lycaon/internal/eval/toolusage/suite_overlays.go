package toolusage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/lycaon/lycaon/internal/harnessfixture"
)

func (c *liveClient) prepareOverlays(ctx context.Context, spec *SuiteCase, result *CaseReport) error {
	if spec.Setup == nil {
		return nil
	}
	body, err := json.Marshal(harnessfixture.Request{SessionID: result.SessionID, Setup: *spec.Setup})
	if err != nil {
		return err
	}
	request, err := c.newRequest(ctx, http.MethodPost, "/harness/overlays", bytes.NewReader(body))
	if err != nil {
		return err
	}
	evidence, err := decodeJSON[harnessfixture.Evidence](c.do(request)) //nolint:bodyclose // decodeJSON closes the body
	if err != nil {
		return err
	}
	result.PreparedOverlays = &evidence
	spec.Prompt += "\nReturned worker changes:"
	for _, overlay := range evidence.Overlays {
		spec.Prompt += "\n" + overlay.Label + ": job " + overlay.JobID + ", session " + overlay.ChildSessionID
	}
	return nil
}
