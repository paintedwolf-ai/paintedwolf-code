package runstate

import "github.com/lycaon/lycaon/pkg/api"

func ValidateWorkflowMode(mode api.SessionMode, requiresAgent bool) error {
	if mode == api.SessionModeAsk && requiresAgent {
		return ErrAskWorkflowRequiresAgent
	}
	return nil
}
