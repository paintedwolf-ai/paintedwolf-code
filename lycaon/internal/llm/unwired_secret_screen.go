package llm

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

type unwiredSecretScreen struct{}

func (unwiredSecretScreen) Screen(context.Context, ScreenDestination, modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
	return modelcall.CompletionRequest{}, &ModelRequestSecretScreenFaultError{Stage: secretmatch.FaultStageScreenUnwired}
}
