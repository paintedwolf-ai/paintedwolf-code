package promptinput

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func VisibleIntent(msg api.Message) bool {
	return api.IsUserIntentMessage(msg) &&
		msg.Origin == api.MessageOriginUser &&
		strings.TrimSpace(msg.Content) != ""
}
