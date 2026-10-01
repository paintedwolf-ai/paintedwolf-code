package session

import "github.com/lycaon/lycaon/internal/hitl"

func composeSandboxSecretCard(card sandboxAskCard, permission *hitl.SecretPermission) (sandboxAskCard, error) {
	plan, err := hitl.ComposeSecretPermission(card.Plan, card.Action, permission, hitl.FaceContext{})
	if err != nil {
		return sandboxAskCard{}, err
	}
	card.Plan = plan
	return card, nil
}
