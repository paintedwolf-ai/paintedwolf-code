package surface

import (
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

func visibleTurnMessage(content string) api.Message {
	return api.Message{
		Role:       api.MessageRoleUser,
		Content:    content,
		Origin:     api.MessageOriginUser,
		Visibility: api.MessageVisibilityTranscript,
	}
}

func hostLoopTurnMessage() api.Message {
	return api.Message{
		Role:       api.MessageRoleUser,
		Content:    HostLoopWakeSentinel,
		Origin:     api.MessageOriginHost,
		Visibility: api.MessageVisibilityInternal,
		Kind:       api.MessageKindHostLoopWake,
	}
}

func workerFinishedTurnMessage(content string) api.Message {
	return api.Message{
		Role:         api.MessageRoleUser,
		Content:      content,
		Origin:       api.MessageOriginHost,
		Visibility:   api.MessageVisibilityInternal,
		Kind:         api.MessageKindHostKick,
		HostSignalID: anchor.WorkerTaskFinished.String(),
	}
}

func guidanceTurnMessage(content, code string) api.Message {
	return api.Message{
		Role:         api.MessageRoleUser,
		Content:      content,
		Origin:       api.MessageOriginHost,
		Visibility:   api.MessageVisibilityInternal,
		Kind:         api.MessageKindCoordinatorGuidance,
		HostSignalID: code,
	}
}

func hostNudgeTurnMessage(content string) api.Message {
	return api.Message{
		Role:       api.MessageRoleUser,
		Content:    content,
		Origin:     api.MessageOriginHost,
		Visibility: api.MessageVisibilityInternal,
		Kind:       api.MessageKindHostNudge,
	}
}
