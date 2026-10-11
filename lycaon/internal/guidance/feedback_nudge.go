package guidance

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// GuidanceNudge carries structured feedback and optional policy copy.
type GuidanceNudge struct {
	Code    string
	Data    map[string]any
	Copy    map[string]string
	Subject *api.FeedbackSubject
}

// ErrGroundingNudge is a non-fatal grounding warning injected after a turn.
// Code is the first fired identifier ([OAR-EVAL-19]). Items is every advisory.
type ErrGroundingNudge struct {
	Code  string
	Data  map[string]any
	Copy  map[string]string
	Items []GuidanceNudge
}

func (e *ErrGroundingNudge) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// Nudges returns Items, or Code when Items is empty.
func (e *ErrGroundingNudge) Nudges() []GuidanceNudge {
	if e == nil {
		return nil
	}
	if len(e.Items) > 0 {
		return e.Items
	}
	if strings.TrimSpace(e.Code) == "" {
		return nil
	}
	return []GuidanceNudge{{Code: e.Code, Data: e.Data, Copy: e.Copy}}
}
