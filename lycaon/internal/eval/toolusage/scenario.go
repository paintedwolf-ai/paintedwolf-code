package toolusage

import (
	"context"
	"fmt"
	"net/http"
	"time"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func (c *liveClient) runScenario(ctx context.Context, sessionID string, task CorpusTask, timeout time.Duration) error {
	if err := c.postPromptAndWait(ctx, sessionID, task.Prompt, timeout); err != nil {
		return err
	}
	for i, followUp := range task.FollowUps {
		if c.onFollowUp != nil {
			c.onFollowUp()
		}
		if followUp.CompactBefore {
			if err := c.compactScenario(ctx, sessionID); err != nil {
				return fmt.Errorf("follow-up %d compaction: %w", i+1, err)
			}
		}
		if err := c.postPromptAndWait(ctx, sessionID, followUp.Prompt, timeout); err != nil {
			return fmt.Errorf("follow-up %d: %w", i+1, err)
		}
	}
	return nil
}

func (c *liveClient) compactScenario(ctx context.Context, sessionID string) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/sessions/"+sessionID+"/compact", nil)
	if err != nil {
		return err
	}
	report, err := decodeJSON[wire.SessionCompactResponse](c.do(req)) //nolint:bodyclose // decodeJSON closes the body.
	if err != nil {
		return err
	}
	if report.Generation == 0 || report.TokensAfter >= report.TokensBefore {
		return fmt.Errorf("compaction made no reduction (generation=%d, tokens=%d→%d); use sufficient prior context and a bounded test compaction configuration", report.Generation, report.TokensBefore, report.TokensAfter)
	}
	return nil
}
