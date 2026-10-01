package browser

import (
	"encoding/json"

	"github.com/go-rod/rod"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// effectSteps are the drive steps that send input; their results report what
// the page did in response.
var effectSteps = map[string]bool{
	"click": true, "hover": true, "drag": true, "scroll": true,
	"fill": true, "type": true, "select": true, "press": true,
}

// actionEffect is one step's recording in the page driver.
type actionEffect struct {
	id       int
	startErr error
}

func beginActionEffect(page *rod.Page) actionEffect {
	raw, err := driverCall(page, "beginEffect", map[string]any{})
	if err != nil {
		return actionEffect{startErr: err}
	}
	var started struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(raw, &started); err != nil {
		return actionEffect{startErr: err}
	}
	return actionEffect{id: started.ID}
}

// attach adds the step's effect to its result. A recording that could not
// start or finish is reported as unrecorded, never omitted.
func (e actionEffect) attach(page *rod.Page, res json.RawMessage) (json.RawMessage, error) {
	var result map[string]json.RawMessage
	if err := json.Unmarshal(res, &result); err != nil {
		return res, err
	}
	effect, err := e.finish(page)
	if err != nil {
		unrecorded, _ := surveyjson.Marshal(map[string]any{"recorded": false, "reason": err.Error()})
		effect = unrecorded
	}
	result["effect"] = effect
	return surveyjson.Marshal(result)
}

func (e actionEffect) finish(page *rod.Page) (json.RawMessage, error) {
	if e.startErr != nil {
		return nil, e.startErr
	}
	raw, err := driverCall(page, "endEffect", map[string]any{"id": e.id})
	if err != nil {
		return nil, err
	}
	var effect map[string]json.RawMessage
	if err := json.Unmarshal(raw, &effect); err != nil {
		return nil, err
	}
	delete(effect, "ok")
	return surveyjson.Marshal(effect)
}
