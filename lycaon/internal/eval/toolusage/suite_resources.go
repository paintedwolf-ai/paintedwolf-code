package toolusage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/lycaon/lycaon/internal/harnessfixture"
)

func (c *liveClient) externalWriteRoot(ctx context.Context, capture, project string) (string, error) {
	body, err := json.Marshal(harnessfixture.WriteResourceRequest{Capture: capture, Project: project})
	if err != nil {
		return "", err
	}
	request, err := c.newRequest(ctx, http.MethodPost, "/harness/write-resource", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	resource, err := decodeJSON[harnessfixture.WriteResource](c.do(request)) //nolint:bodyclose // decodeJSON closes the body.
	return resource.Path, err
}
