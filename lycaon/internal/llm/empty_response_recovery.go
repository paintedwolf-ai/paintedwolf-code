package llm

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// Recovery changes only the suffix of the already admitted request. It never
// creates an additional retry allowance or includes provider output.
func appendEmptyResponseRecovery(ctx context.Context, req *modelcall.CompletionRequest) error {
	render := req.EmptyResponseRecovery
	if render == nil {
		return nil
	}
	req.EmptyResponseRecovery = nil
	body, err := render(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return nil
	}
	if len(body) > 4096 {
		return fmt.Errorf("empty response recovery exceeds 4096 bytes")
	}
	req.Messages = append(slices.Clone(req.Messages), api.Message{
		Role: api.MessageRoleUser, Content: body,
		Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem,
		TrustTier: api.ContentTrustTierTrusted, Visibility: api.MessageVisibilityInternal,
	})
	return nil
}
