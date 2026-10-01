package toolusage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/harnessfixture"
)

func (c *liveClient) installPrelude(ctx context.Context, spec *SuiteCase, result *CaseReport) error {
	if len(spec.Prelude) == 0 {
		return nil
	}
	plan := harnessfixture.Prelude{OperationID: uuid.NewString(), Steps: spec.Prelude, Final: spec.PreludeFinal}
	if result.Sandbox != nil {
		body, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		// Expansion is confined to the suite's declared fixture placeholders.
		path, err := json.Marshal(result.Sandbox.Path)
		if err != nil {
			return err
		}
		body = []byte(strings.ReplaceAll(string(body), "${sandbox_path}", string(path[1:len(path)-1])))
		body = []byte(strings.ReplaceAll(string(body), "${sandbox_port}", strconv.Itoa(result.Sandbox.Port)))
		body = []byte(strings.ReplaceAll(string(body), `"${sandbox_port_number}"`, strconv.Itoa(result.Sandbox.Port)))
		if err := json.Unmarshal(body, &plan); err != nil {
			return err
		}
		for _, step := range plan.Steps {
			if step.ID == spec.SandboxStep {
				result.Sandbox.PreparationCallID = plan.CallID(step)
			}
		}
		if result.Sandbox.PreparationCallID == "" && spec.SandboxStep != "" {
			return fmt.Errorf("sandbox requires a declared preparation step")
		}
	}
	return c.scriptedResponse(ctx, "/harness/preparation", map[string]any{"session_id": result.SessionID, "prelude": plan})
}

func (c *liveClient) preludeReceipt(ctx context.Context, sessionID string) (harnessfixture.PreludeReceipt, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/harness/preparation/"+sessionID, nil)
	if err != nil {
		return harnessfixture.PreludeReceipt{}, err
	}
	return decodeJSON[harnessfixture.PreludeReceipt](c.do(req)) //nolint:bodyclose // decodeJSON closes the body.
}
