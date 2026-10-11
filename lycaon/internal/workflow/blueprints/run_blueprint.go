package blueprints

import (
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprintfile"
	"strings"
)

func blueprintTitleFromSlashText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	firstSpace := strings.IndexAny(text, " \t\r\n")
	if firstSpace < 0 {
		return ""
	}
	return blueprint.SlugTitle(strings.TrimSpace(text[firstSpace+1:]))
}

func mintBlueprintTitle(planName, userIntentSlug string) string {
	title := strings.TrimSpace(planName)
	if !blueprintfile.IsPlaceholderTitle(title) {
		return title
	}
	if slug := strings.TrimSpace(userIntentSlug); slug != "" {
		return slug
	}
	return blueprintfile.PlaceholderTitle
}

// ValidatePlanApprovalReady refreshes readiness without satisfying the gate.

// SyncPlanApproved records approval after the blueprint is durable.

// ApprovePlan binds approval to reviewed bytes and revision.
