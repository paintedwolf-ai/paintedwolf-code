package browser

import (
	"encoding/json"

	"github.com/go-rod/rod"
	"github.com/lycaon/lycaon/internal/browserengine"
)

type completedAction struct {
	Index             int    `json:"index"`
	Type              string `json:"type"`
	Locators          string `json:"locators,omitempty"`
	LocatorsTruncated bool   `json:"locators_truncated,omitempty"`
}

func runCaptureActions(page *rod.Page, drive *pageDrive, actions []CaptureAction, idle bool, afterAction func(CaptureAction, json.RawMessage) error) ([]json.RawMessage, error) {
	return runActionBatch(actions, func(act CaptureAction) (json.RawMessage, error) {
		if idle && act.Wait == "" {
			act.Wait = DefaultWaitIdle
		}
		return runAction(page, drive, act)
	}, afterAction)
}

// A rejected batch preserves its completed prefix; the failing action may also
// have taken effect before its settling step failed.
func runActionBatch(actions []CaptureAction, run func(CaptureAction) (json.RawMessage, error), afterAction func(CaptureAction, json.RawMessage) error) ([]json.RawMessage, error) {
	if err := validateActions(actions); err != nil {
		return nil, err
	}
	results := make([]json.RawMessage, 0, len(actions))
	completed := make([]completedAction, 0, len(actions))
	for i, act := range actions {
		res, err := run(act)
		if err != nil || !driverOK(res) {
			data := actionFailedData(i, act, res, err)
			data["completed_actions"] = completed
			return results, browserengine.Reject("CAPTURE_ACTION_FAILED", data)
		}
		results = append(results, res)
		locators, clipped := truncateUTF8Bytes(locatorSummary(act), 256)
		completed = append(completed, completedAction{Index: i, Type: act.Type, Locators: locators, LocatorsTruncated: clipped})
		if afterAction != nil {
			if err := afterAction(act, res); err != nil {
				return results, err
			}
		}
	}
	return results, nil
}
