package browser

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

const (
	// pointerMoveSteps interpolates a move so hover, enter, and leave handlers fire along the path.
	pointerMoveSteps = 8
	// dragSteps and dragStepInterval pace a drag the way a hand does; drag libraries read both.
	dragSteps        = 16
	dragStepInterval = 16 * time.Millisecond
	// wheelNotchPx and wheelNotchInterval split a scroll into the events a mouse wheel sends.
	wheelNotchPx       = 100
	wheelNotchInterval = 16 * time.Millisecond
	maxClickCount      = 3
)

// pageDrive is the input state a held page keeps between tool calls: where the
// pointer rests, and the request routes its fetch handler answers from.
type pageDrive struct {
	mu      sync.Mutex
	pointer proto.Point
	routes  *routeTable
}

func newPageDrive(routes *routeTable) *pageDrive {
	return &pageDrive{routes: routes}
}

func (d *pageDrive) position() proto.Point {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pointer
}

func (d *pageDrive) place(p proto.Point) {
	d.mu.Lock()
	d.pointer = p
	d.mu.Unlock()
}

// aimResult is where the driver resolved a target for pointer input.
type aimResult struct {
	OK     bool            `json:"ok"`
	Point  proto.Point     `json:"point"`
	Reach  json.RawMessage `json:"reach,omitempty"`
	Target json.RawMessage `json:"target,omitempty"`
	Rect   json.RawMessage `json:"rect,omitempty"`
}

// aim resolves a locator to the point trusted input should land on. A failed aim
// returns the driver's report for the action result.
func aim(page *rod.Page, loc ActionLocator, require string, timeoutMS int) (aimResult, json.RawMessage, error) {
	opts := loc.driverArgs()
	opts["require"] = require
	raw, err := driverCall(page, "aim", withTimeout(opts, timeoutMS))
	if err != nil || !driverOK(raw) {
		return aimResult{}, raw, err
	}
	var out aimResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return aimResult{}, nil, fmt.Errorf("decode aim: %w", err)
	}
	return out, raw, nil
}

func mouseButton(name string) (proto.InputMouseButton, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "left":
		return proto.InputMouseButtonLeft, nil
	case "right":
		return proto.InputMouseButtonRight, nil
	case "middle":
		return proto.InputMouseButtonMiddle, nil
	default:
		return "", fmt.Errorf("unknown mouse button %q", name)
	}
}

// buttonsMask is the DOM `buttons` bitmask for a held button.
func buttonsMask(b proto.InputMouseButton) int {
	switch b {
	case proto.InputMouseButtonRight:
		return 2
	case proto.InputMouseButtonMiddle:
		return 4
	default:
		return 1
	}
}

type mouseEvent struct {
	kind      proto.InputDispatchMouseEventType
	at        proto.Point
	button    proto.InputMouseButton
	buttons   int
	clicks    int
	modifiers int
	delta     proto.Point
}

func dispatchMouse(page *rod.Page, e mouseEvent) error {
	button := e.button
	if button == "" {
		button = proto.InputMouseButtonNone
	}
	buttons := e.buttons
	return proto.InputDispatchMouseEvent{
		Type: e.kind, X: e.at.X, Y: e.at.Y, Button: button, Buttons: &buttons,
		ClickCount: e.clicks, Modifiers: e.modifiers, DeltaX: e.delta.X, DeltaY: e.delta.Y,
	}.Call(page)
}

// moveTo glides the pointer from where it rests, carrying any held button.
func moveTo(page *rod.Page, drive *pageDrive, to proto.Point, steps int, held proto.InputMouseButton, interval time.Duration) error {
	from := drive.position()
	buttons := 0
	if held != "" {
		buttons = buttonsMask(held)
	}
	for i := 1; i <= steps; i++ {
		f := float64(i) / float64(steps)
		p := proto.Point{X: from.X + (to.X-from.X)*f, Y: from.Y + (to.Y-from.Y)*f}
		if err := dispatchMouse(page, mouseEvent{kind: proto.InputDispatchMouseEventTypeMouseMoved, at: p, button: held, buttons: buttons}); err != nil {
			return err
		}
		drive.place(p)
		if err := pause(page, interval); err != nil {
			return err
		}
	}
	return nil
}

func pointerResult(aimed aimResult, extra map[string]any) (json.RawMessage, error) {
	out := map[string]any{"ok": true, "point": aimed.Point}
	if len(aimed.Target) > 0 {
		out["target"] = aimed.Target
	}
	if len(aimed.Reach) > 0 {
		out["reach"] = aimed.Reach
	}
	if len(aimed.Rect) > 0 {
		out["rect"] = aimed.Rect
	}
	for k, v := range extra {
		out[k] = v
	}
	return surveyjson.Marshal(out)
}

func clickAction(page *rod.Page, drive *pageDrive, act CaptureAction) (json.RawMessage, error) {
	button, err := mouseButton(act.Button)
	if err != nil {
		return nil, err
	}
	count := act.Count
	if count <= 0 {
		count = 1
	}
	if count > maxClickCount {
		return nil, fmt.Errorf("click count %d exceeds %d", count, maxClickCount)
	}
	aimed, report, err := aim(page, act.ActionLocator, "receive", act.TimeoutMS)
	if err != nil || !aimed.OK {
		return report, err
	}
	if err := moveTo(page, drive, aimed.Point, pointerMoveSteps, "", 0); err != nil {
		return nil, err
	}
	held, releases, err := holdModifiers(page, act.Modifiers)
	if err != nil {
		return nil, err
	}
	defer releases()
	// Each press of a multi-click carries its own count, as a browser reports a double click.
	for n := 1; n <= count; n++ {
		for _, kind := range []proto.InputDispatchMouseEventType{proto.InputDispatchMouseEventTypeMousePressed, proto.InputDispatchMouseEventTypeMouseReleased} {
			buttons := buttonsMask(button)
			if kind == proto.InputDispatchMouseEventTypeMouseReleased {
				buttons = 0
			}
			if err := dispatchMouse(page, mouseEvent{kind: kind, at: aimed.Point, button: button, buttons: buttons, clicks: n, modifiers: held}); err != nil {
				return nil, err
			}
		}
	}
	return pointerResult(aimed, nil)
}

func hoverAction(page *rod.Page, drive *pageDrive, act CaptureAction) (json.RawMessage, error) {
	aimed, report, err := aim(page, act.ActionLocator, "receive", act.TimeoutMS)
	if err != nil || !aimed.OK {
		return report, err
	}
	if err := moveTo(page, drive, aimed.Point, pointerMoveSteps, "", 0); err != nil {
		return nil, err
	}
	if err := pause(page, holdDuration(act.HoldMS)); err != nil {
		return nil, err
	}
	return pointerResult(aimed, nil)
}

func dragAction(page *rod.Page, drive *pageDrive, act CaptureAction) (json.RawMessage, error) {
	if (act.To == nil || act.To.empty()) == (act.By == nil) {
		return nil, fmt.Errorf("drag requires exactly one of to or by")
	}
	from, report, err := aim(page, act.ActionLocator, "receive", act.TimeoutMS)
	if err != nil || !from.OK {
		return report, err
	}
	if err := moveTo(page, drive, from.Point, pointerMoveSteps, "", 0); err != nil {
		return nil, err
	}
	press := mouseEvent{kind: proto.InputDispatchMouseEventTypeMousePressed, at: from.Point, button: proto.InputMouseButtonLeft, buttons: 1, clicks: 1}
	if err := dispatchMouse(page, press); err != nil {
		return nil, err
	}
	release := func(at proto.Point) error {
		return dispatchMouse(page, mouseEvent{kind: proto.InputDispatchMouseEventTypeMouseReleased, at: at, button: proto.InputMouseButtonLeft, clicks: 1})
	}
	if err := pause(page, holdDuration(act.HoldMS)); err != nil {
		_ = release(from.Point)
		return nil, err
	}
	var dest proto.Point
	var dropTarget json.RawMessage
	if act.By != nil {
		dest = proto.Point{X: from.Point.X + act.By.X, Y: from.Point.Y + act.By.Y}
	} else {
		// The dragged element may cover its drop target, so the target only needs to be visible.
		to, toReport, toErr := aim(page, *act.To, "visible", act.TimeoutMS)
		if toErr != nil || !to.OK {
			_ = release(drive.position())
			return toReport, toErr
		}
		dest, dropTarget = to.Point, to.Target
	}
	if err := moveTo(page, drive, dest, dragSteps, proto.InputMouseButtonLeft, dragStepInterval); err != nil {
		_ = release(drive.position())
		return nil, err
	}
	if err := release(dest); err != nil {
		return nil, err
	}
	extra := map[string]any{"drop_point": dest}
	if len(dropTarget) > 0 {
		extra["drop_target"] = dropTarget
	}
	return pointerResult(from, extra)
}

func scrollAction(page *rod.Page, drive *pageDrive, act CaptureAction) (json.RawMessage, error) {
	if act.By == nil || (act.By.X == 0 && act.By.Y == 0) {
		return nil, fmt.Errorf("scroll requires a nonzero by offset")
	}
	aimed := aimResult{OK: true}
	if act.empty() {
		// Without a target the wheel turns over the middle of the viewport.
		width, height := viewportOf(page)
		aimed.Point = proto.Point{X: float64(width) / 2, Y: float64(height) / 2}
	} else {
		var report json.RawMessage
		var err error
		// A wheel scrolls whatever sits under the pointer, so the target needs only to be visible.
		aimed, report, err = aim(page, act.ActionLocator, "visible", act.TimeoutMS)
		if err != nil || !aimed.OK {
			return report, err
		}
	}
	if err := moveTo(page, drive, aimed.Point, pointerMoveSteps, "", 0); err != nil {
		return nil, err
	}
	notches := int(math.Ceil(math.Max(math.Abs(act.By.X), math.Abs(act.By.Y)) / wheelNotchPx))
	step := proto.Point{X: act.By.X / float64(notches), Y: act.By.Y / float64(notches)}
	for i := 0; i < notches; i++ {
		if err := dispatchMouse(page, mouseEvent{kind: proto.InputDispatchMouseEventTypeMouseWheel, at: aimed.Point, delta: step}); err != nil {
			return nil, err
		}
		if err := pause(page, wheelNotchInterval); err != nil {
			return nil, err
		}
	}
	return pointerResult(aimed, map[string]any{"scrolled_by": act.By})
}

func holdDuration(ms int) time.Duration {
	return time.Duration(min(max(ms, 0), MaxHoldMS)) * time.Millisecond
}
