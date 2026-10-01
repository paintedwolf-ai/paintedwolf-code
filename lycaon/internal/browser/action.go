package browser

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

const (
	MaxCaptureActions = 20
	DefaultWaitIdle   = "idle"
	NavTimeout        = 30 * time.Second
	ActionTimeout     = 15 * time.Second
	// MaxTypedRunes bounds one type or fill value.
	MaxTypedRunes = 4096
	// MaxHoldMS bounds a hover dwell or drag hold.
	MaxHoldMS = 5000
)

// ActionLocator names an element the way a reader finds it. Precedence: testid,
// selector, role with label or text, label, then text.
type ActionLocator struct {
	Testid   string `json:"testid,omitempty"`
	Selector string `json:"selector,omitempty"`
	Role     string `json:"role,omitempty"`
	Label    string `json:"label,omitempty"`
	Text     string `json:"text,omitempty"`
}

func (l ActionLocator) empty() bool {
	return strings.TrimSpace(l.Testid+l.Selector+l.Role+l.Label+l.Text) == ""
}

func (l ActionLocator) driverArgs() map[string]any {
	return map[string]any{"testid": l.Testid, "selector": l.Selector, "role": l.Role, "label": l.Label, "text": l.Text}
}

// ActionOffset is a distance in CSS pixels.
type ActionOffset struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// CaptureAction is one bounded drive step. Pointer and keyboard steps reach the
// page as trusted browser input at the point the target actually receives.
type CaptureAction struct {
	Type string `json:"type"`
	ActionLocator
	Value     string         `json:"value,omitempty"`
	Key       string         `json:"key,omitempty"`
	Modifiers []string       `json:"modifiers,omitempty"`
	Button    string         `json:"button,omitempty"`
	Count     int            `json:"count,omitempty"`
	To        *ActionLocator `json:"to,omitempty"`
	By        *ActionOffset  `json:"by,omitempty"`
	HoldMS    int            `json:"hold_ms,omitempty"`
	Routes    []RouteRule    `json:"routes,omitempty"`
	TimeoutMS int            `json:"timeout_ms,omitempty"`
	Wait      string         `json:"wait,omitempty"`
}

func (a CaptureAction) kind() string {
	return strings.ToLower(strings.TrimSpace(a.Type))
}

func runAction(page *rod.Page, drive *pageDrive, act CaptureAction) (json.RawMessage, error) {
	if !effectSteps[act.kind()] {
		res, err := dispatchAction(page, drive, act)
		if err != nil {
			return res, err
		}
		return settleAction(page, act, res)
	}
	effect := beginActionEffect(page)
	res, err := dispatchAction(page, drive, act)
	if err != nil {
		return res, err
	}
	if res, err = settleAction(page, act, res); err != nil || !driverOK(res) {
		return res, err
	}
	return effect.attach(page, res)
}

func dispatchAction(page *rod.Page, drive *pageDrive, act CaptureAction) (json.RawMessage, error) {
	switch act.kind() {
	case "click":
		return clickAction(page, drive, act)
	case "hover":
		return hoverAction(page, drive, act)
	case "drag":
		return dragAction(page, drive, act)
	case "scroll":
		return scrollAction(page, drive, act)
	case "fill":
		return fillAction(page, drive, act)
	case "type":
		return typeAction(page, drive, act)
	case "select":
		opts := act.driverArgs()
		opts["value"] = act.Value
		return driverCall(page, "select", withTimeout(opts, act.TimeoutMS))
	case "press":
		return pressAction(page, act)
	case "route":
		return routeAction(drive, act)
	case "wait_for":
		return driverCall(page, "waitFor", withTimeout(map[string]any{"selector": act.Selector, "text": act.Text}, act.TimeoutMS))
	case "wait":
		return waitAction(page, act)
	default:
		return nil, fmt.Errorf("unknown action type %q", act.Type)
	}
}

func withTimeout(opts map[string]any, timeoutMS int) map[string]any {
	if timeoutMS > 0 {
		opts["timeout_ms"] = timeoutMS
	}
	return opts
}

func waitAction(page *rod.Page, act CaptureAction) (json.RawMessage, error) {
	wait := strings.TrimSpace(act.Wait)
	if wait == "" || wait == DefaultWaitIdle {
		return driverCall(page, "waitIdle", map[string]any{})
	}
	if ms := act.TimeoutMS; ms > 0 {
		if err := pause(page, time.Duration(ms)*time.Millisecond); err != nil {
			return nil, err
		}
		return surveyjson.Marshal(map[string]any{"ok": true, "waited_ms": ms})
	}
	return nil, fmt.Errorf("wait action requires wait=idle or timeout_ms")
}

func pause(page *rod.Page, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-page.GetContext().Done():
		return page.GetContext().Err()
	case <-timer.C:
		return nil
	}
}

func settleAction(page *rod.Page, act CaptureAction, res json.RawMessage) (json.RawMessage, error) {
	if !driverOK(res) || act.kind() == "wait" || strings.TrimSpace(act.Wait) != DefaultWaitIdle {
		return res, nil
	}
	idleRes, err := driverCall(page, "waitIdle", map[string]any{})
	if err != nil {
		return idleRes, err
	}
	if !driverOK(idleRes) {
		return idleRes, nil
	}
	var result, settled map[string]json.RawMessage
	if err := json.Unmarshal(res, &result); err != nil {
		return res, err
	}
	if err := json.Unmarshal(idleRes, &settled); err != nil {
		return idleRes, err
	}
	if state, ok := settled["state"]; ok {
		result["state"] = state
	}
	return surveyjson.Marshal(result)
}
