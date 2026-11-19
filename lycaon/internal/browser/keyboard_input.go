package browser

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// keyStroke is one key with the modifiers held while it is pressed.
type keyStroke struct {
	modifiers []modifierKey
	key       input.KeyInfo
}

type modifierKey struct {
	name string
	info input.KeyInfo
	bit  int
}

var modifierKeys = map[string]modifierKey{
	"alt":     {name: "Alt", info: input.AltLeft.Info(), bit: input.ModifierAlt},
	"control": {name: "Control", info: input.ControlLeft.Info(), bit: input.ModifierControl},
	"meta":    {name: "Meta", info: input.MetaLeft.Info(), bit: input.ModifierMeta},
	"shift":   {name: "Shift", info: input.ShiftLeft.Info(), bit: input.ModifierShift},
}

var namedKeys = map[string]input.Key{
	"enter": input.Enter, "tab": input.Tab, "escape": input.Escape, "backspace": input.Backspace,
	"delete": input.Delete, "space": input.Space, "insert": input.Insert,
	"arrowup": input.ArrowUp, "arrowdown": input.ArrowDown, "arrowleft": input.ArrowLeft, "arrowright": input.ArrowRight,
	"home": input.Home, "end": input.End, "pageup": input.PageUp, "pagedown": input.PageDown,
	"f1": input.F1, "f2": input.F2, "f3": input.F3, "f4": input.F4, "f5": input.F5, "f6": input.F6,
	"f7": input.F7, "f8": input.F8, "f9": input.F9, "f10": input.F10, "f11": input.F11, "f12": input.F12,
}

// modifierFor resolves a modifier name. Mod is the platform's shortcut modifier:
// Meta on macOS, where the managed browser reports a Mac platform, Control elsewhere.
func modifierFor(name string) (modifierKey, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "mod" {
		n = "control"
		if runtime.GOOS == "darwin" {
			n = "meta"
		}
	}
	if n == "ctrl" || n == "cmd" || n == "option" {
		n = map[string]string{"ctrl": "control", "cmd": "meta", "option": "alt"}[n]
	}
	m, ok := modifierKeys[n]
	return m, ok
}

// parseChord reads "Enter", "a", or "Mod+Shift+K": modifiers first, one key last.
func parseChord(chord string) (keyStroke, error) {
	chord = strings.TrimSpace(chord)
	parts := strings.Split(chord, "+")
	// A bare "+" or a chord ending in "+" names the plus key itself.
	if strings.HasSuffix(chord, "++") || chord == "+" {
		parts = append(parts[:len(parts)-2], "+")
	}
	var stroke keyStroke
	for i, part := range parts {
		if i < len(parts)-1 {
			m, ok := modifierFor(part)
			if !ok {
				return keyStroke{}, fmt.Errorf("unknown modifier %q in %q", part, chord)
			}
			stroke.modifiers = append(stroke.modifiers, m)
			continue
		}
		info, err := keyInfo(part)
		if err != nil {
			return keyStroke{}, fmt.Errorf("%w in %q", err, chord)
		}
		stroke.key = info
	}
	return stroke, nil
}

func keyInfo(name string) (input.KeyInfo, error) {
	if k, ok := namedKeys[strings.ToLower(name)]; ok {
		return k.Info(), nil
	}
	if m, ok := modifierFor(name); ok {
		return m.info, nil
	}
	if utf8.RuneCountInString(name) == 1 {
		r, _ := utf8.DecodeRuneInString(name)
		if info, ok := printableKeyInfo(r); ok {
			return info, nil
		}
		return input.KeyInfo{Key: name}, nil
	}
	return input.KeyInfo{}, fmt.Errorf("unknown key %q", name)
}

// printableKeyInfo carries the physical key for ASCII characters the key map defines.
func printableKeyInfo(r rune) (info input.KeyInfo, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return input.Key(r).Info(), true
}

// holdModifiers presses the named modifiers and returns their mask and a release.
func holdModifiers(page *rod.Page, names []string) (int, func(), error) {
	mask := 0
	var held []modifierKey
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			mask &^= held[i].bit
			_ = keyEvent(proto.InputDispatchKeyEventTypeKeyUp, held[i].info, mask, "").Call(page)
		}
	}
	for _, name := range names {
		m, ok := modifierFor(name)
		if !ok {
			release()
			return 0, nil, fmt.Errorf("unknown modifier %q", name)
		}
		mask |= m.bit
		if err := keyEvent(proto.InputDispatchKeyEventTypeRawKeyDown, m.info, mask, "").Call(page); err != nil {
			release()
			return 0, nil, err
		}
		held = append(held, m)
	}
	return mask, release, nil
}

func keyEvent(kind proto.InputDispatchKeyEventType, info input.KeyInfo, modifiers int, text string) *proto.InputDispatchKeyEvent {
	return &proto.InputDispatchKeyEvent{
		Type: kind, Modifiers: modifiers, Key: info.Key, Code: info.Code,
		WindowsVirtualKeyCode: info.KeyCode, Text: text, UnmodifiedText: text,
	}
}

// pressStroke sends one chord. Printable keys insert text unless a command modifier is held,
// and macOS receives the editing command a native text field would run.
func pressStroke(page *rod.Page, stroke keyStroke) error {
	mask := 0
	for _, m := range stroke.modifiers {
		mask |= m.bit
		if err := keyEvent(proto.InputDispatchKeyEventTypeRawKeyDown, m.info, mask, "").Call(page); err != nil {
			return err
		}
	}
	text := ""
	if utf8.RuneCountInString(stroke.key.Key) == 1 && mask&^input.ModifierShift == 0 {
		text = stroke.key.Key
	}
	down := keyEvent(proto.InputDispatchKeyEventTypeKeyDown, stroke.key, mask, text)
	if text == "" {
		down.Type = proto.InputDispatchKeyEventTypeRawKeyDown
	}
	if runtime.GOOS == "darwin" {
		down.Commands = macEditingCommands[editingCommandKey(stroke)]
	}
	err := down.Call(page)
	if upErr := keyEvent(proto.InputDispatchKeyEventTypeKeyUp, stroke.key, mask, "").Call(page); err == nil {
		err = upErr
	}
	for i := len(stroke.modifiers) - 1; i >= 0; i-- {
		mask &^= stroke.modifiers[i].bit
		if upErr := keyEvent(proto.InputDispatchKeyEventTypeKeyUp, stroke.modifiers[i].info, mask, "").Call(page); err == nil {
			err = upErr
		}
	}
	return err
}

func editingCommandKey(stroke keyStroke) string {
	parts := make([]string, 0, len(stroke.modifiers)+1)
	for _, name := range []string{"Shift", "Control", "Alt", "Meta"} {
		for _, m := range stroke.modifiers {
			if m.name == name {
				parts = append(parts, name)
			}
		}
	}
	return strings.Join(append(parts, stroke.key.Code), "+")
}

// macEditingCommands are the Cocoa selectors a macOS text field runs for common chords.
var macEditingCommands = map[string][]string{
	"Backspace": {"deleteBackward"}, "Delete": {"deleteForward"}, "Enter": {"insertNewline"},
	"ArrowUp": {"moveUp"}, "ArrowDown": {"moveDown"}, "ArrowLeft": {"moveLeft"}, "ArrowRight": {"moveRight"},
	"Shift+ArrowUp": {"moveUpAndModifySelection"}, "Shift+ArrowDown": {"moveDownAndModifySelection"},
	"Shift+ArrowLeft": {"moveLeftAndModifySelection"}, "Shift+ArrowRight": {"moveRightAndModifySelection"},
	"Alt+Backspace": {"deleteWordBackward"}, "Alt+Delete": {"deleteWordForward"},
	"Alt+ArrowLeft": {"moveWordLeft"}, "Alt+ArrowRight": {"moveWordRight"},
	"Meta+Backspace": {"deleteToBeginningOfLine"}, "Meta+ArrowLeft": {"moveToLeftEndOfLine"},
	"Meta+ArrowRight": {"moveToRightEndOfLine"}, "Meta+ArrowUp": {"moveToBeginningOfDocument"},
	"Meta+ArrowDown": {"moveToEndOfDocument"}, "Meta+KeyA": {"selectAll"}, "Meta+KeyC": {"copy"},
	"Meta+KeyX": {"cut"}, "Meta+KeyV": {"paste"}, "Meta+KeyZ": {"undo"}, "Shift+Meta+KeyZ": {"redo"},
}

func pressAction(page *rod.Page, act CaptureAction) (json.RawMessage, error) {
	stroke, err := parseChord(act.Key)
	if err != nil {
		return nil, err
	}
	if err := pressStroke(page, stroke); err != nil {
		return nil, err
	}
	return surveyjson.Marshal(map[string]any{"ok": true, "key": act.Key})
}

// typeText sends each character as its own trusted key press into the focused field.
func typeText(page *rod.Page, text string) error {
	for _, r := range text {
		if r == '\n' {
			if err := pressStroke(page, keyStroke{key: input.Enter.Info()}); err != nil {
				return err
			}
			continue
		}
		info, ok := printableKeyInfo(r)
		if !ok {
			info = input.KeyInfo{Key: string(r)}
		}
		ch := string(r)
		if err := keyEvent(proto.InputDispatchKeyEventTypeKeyDown, info, 0, ch).Call(page); err != nil {
			return err
		}
		if err := keyEvent(proto.InputDispatchKeyEventTypeKeyUp, info, 0, "").Call(page); err != nil {
			return err
		}
	}
	return nil
}

func checkTypedLength(value string) error {
	if n := utf8.RuneCountInString(value); n > MaxTypedRunes {
		return fmt.Errorf("value has %d characters; the limit is %d", n, MaxTypedRunes)
	}
	return nil
}

// focusField presses the target field so it takes focus the way a reader's click gives it.
func focusField(page *rod.Page, drive *pageDrive, act CaptureAction) (aimResult, json.RawMessage, error) {
	opts := act.driverArgs()
	raw, err := driverCall(page, "aimField", withTimeout(opts, act.TimeoutMS))
	if err != nil || !driverOK(raw) {
		return aimResult{}, raw, err
	}
	var aimed aimResult
	if err := json.Unmarshal(raw, &aimed); err != nil {
		return aimResult{}, nil, fmt.Errorf("decode field aim: %w", err)
	}
	if err := moveTo(page, drive, aimed.Point, pointerMoveSteps, "", 0); err != nil {
		return aimResult{}, nil, err
	}
	for _, kind := range []proto.InputDispatchMouseEventType{proto.InputDispatchMouseEventTypeMousePressed, proto.InputDispatchMouseEventTypeMouseReleased} {
		buttons := 1
		if kind == proto.InputDispatchMouseEventTypeMouseReleased {
			buttons = 0
		}
		if err := dispatchMouse(page, mouseEvent{kind: kind, at: aimed.Point, button: proto.InputMouseButtonLeft, buttons: buttons, clicks: 1}); err != nil {
			return aimResult{}, nil, err
		}
	}
	return aimed, raw, nil
}

func focusedFieldReport(page *rod.Page) (map[string]any, error) {
	raw, err := driverCall(page, "focusedField", nil)
	if err != nil {
		return nil, err
	}
	var field map[string]any
	if err := json.Unmarshal(raw, &field); err != nil {
		return nil, fmt.Errorf("decode focused field: %w", err)
	}
	return field, nil
}

// fillAction replaces a field's contents the way a reader does: focus it, select what is
// there, and type over it. An empty value deletes the selection.
func fillAction(page *rod.Page, drive *pageDrive, act CaptureAction) (json.RawMessage, error) {
	if err := checkTypedLength(act.Value); err != nil {
		return nil, err
	}
	aimed, report, err := focusField(page, drive, act)
	if err != nil || !aimed.OK {
		return report, err
	}
	selected, err := driverCall(page, "selectFocusedContents", nil)
	if err != nil {
		return nil, err
	}
	if !driverOK(selected) {
		return selected, nil
	}
	if act.Value == "" {
		err = pressStroke(page, keyStroke{key: input.Backspace.Info()})
	} else {
		err = proto.InputInsertText{Text: act.Value}.Call(page)
	}
	if err != nil {
		return nil, err
	}
	field, err := focusedFieldReport(page)
	if err != nil {
		return nil, err
	}
	return pointerResult(aimed, map[string]any{"field": field})
}

// typeAction types into the located field, or into whatever holds focus when no target is named.
func typeAction(page *rod.Page, drive *pageDrive, act CaptureAction) (json.RawMessage, error) {
	if err := checkTypedLength(act.Value); err != nil {
		return nil, err
	}
	aimed := aimResult{OK: true}
	if !act.empty() {
		var report json.RawMessage
		var err error
		aimed, report, err = focusField(page, drive, act)
		if err != nil || !aimed.OK {
			return report, err
		}
	}
	before, err := focusedFieldReport(page)
	if err != nil {
		return nil, err
	}
	if editable, _ := before["editable"].(bool); !editable {
		return surveyjson.Marshal(map[string]any{"ok": false, "error": "no focused field"})
	}
	if err := typeText(page, act.Value); err != nil {
		return nil, err
	}
	field, err := focusedFieldReport(page)
	if err != nil {
		return nil, err
	}
	if act.empty() {
		return surveyjson.Marshal(map[string]any{"ok": true, "field": field})
	}
	return pointerResult(aimed, map[string]any{"field": field})
}
