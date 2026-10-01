package contribution

import (
	"fmt"
	"sort"
	"strings"
)

// Leader sequences store a symbolic prefix and one bare key.

var chordModifiers = map[string]bool{
	"Mod": true, "Ctrl": true, "Alt": true, "Shift": true, "Super": true,
}

var namedKeys = map[string]bool{
	"Enter": true, "Escape": true, "Tab": true, "Space": true,
	"Backspace": true, "Delete": true,
	"ArrowUp": true, "ArrowDown": true, "ArrowLeft": true, "ArrowRight": true,
	"Home": true, "End": true,
	"PageUp": true, "PageDown": true,
	"CapsLock": true, "Insert": true, "ScrollLock": true,
	"F1": true, "F2": true, "F3": true, "F4": true, "F5": true, "F6": true,
	"F7": true, "F8": true, "F9": true, "F10": true, "F11": true, "F12": true,
}

// validateBinding accepts one chord or one Leader sequence.
func validateBinding(binding string) error {
	binding = strings.TrimSpace(binding)
	if binding == "" {
		return fmt.Errorf("empty binding")
	}
	parts := strings.Fields(binding)
	switch len(parts) {
	case 1:
		return validateChord(parts[0])
	case 2:
		if parts[0] != "Leader" {
			return fmt.Errorf("binding %q: a sequence must start with Leader", binding)
		}
		if strings.Contains(parts[1], "+") {
			return fmt.Errorf("binding %q: the key after Leader must be bare", binding)
		}
		return validateKey(parts[1])
	default:
		return fmt.Errorf("binding %q: at most two steps (Leader + key)", binding)
	}
}

func validateChord(chord string) error {
	if chord == "Leader" {
		return fmt.Errorf("leader alone is not a binding")
	}
	tokens := strings.Split(chord, "+")
	// A literal plus key produces two trailing separators.
	if strings.HasSuffix(chord, "++") {
		tokens = append(tokens[:len(tokens)-2], "+")
	}
	seen := map[string]bool{}
	for i, token := range tokens {
		last := i == len(tokens)-1
		if !last {
			if !chordModifiers[token] {
				return fmt.Errorf("chord %q: unknown modifier %q", chord, token)
			}
			if seen[token] {
				return fmt.Errorf("chord %q: duplicate modifier %q", chord, token)
			}
			seen[token] = true
			continue
		}
		if err := validateKey(token); err != nil {
			return fmt.Errorf("chord %q: %w", chord, err)
		}
	}
	return nil
}

func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("missing key")
	}
	if namedKeys[key] {
		return nil
	}
	if chordModifiers[key] {
		return fmt.Errorf("%q is a modifier, not a key", key)
	}
	runes := []rune(key)
	if len(runes) == 1 && runes[0] > ' ' {
		return nil
	}
	return fmt.Errorf("unknown key %q", key)
}

// Ctrl emits Mod off macOS; macOS has no distinct Super chord.
func bindingProducible(platform, binding string) bool {
	parts := strings.Fields(binding)
	if len(parts) == 2 && parts[0] == "Leader" {
		return true
	}
	if len(parts) != 1 {
		return false
	}
	tokens := strings.Split(parts[0], "+")
	held := map[string]bool{}
	for _, token := range tokens[:len(tokens)-1] {
		held[token] = true
	}
	if platform == "macos" {
		return !held["Super"]
	}
	return !held["Ctrl"]
}

// Conflict identity sorts modifiers and preserves key case.
func chordIdentity(binding string) string {
	return orderModifiers(binding, func(a, b string) bool { return a < b })
}

// Published bindings use a fixed modifier order.
var canonicalOrder = map[string]int{"Mod": 0, "Ctrl": 1, "Alt": 2, "Shift": 3, "Super": 4}

func canonicalChord(binding string) string {
	return orderModifiers(binding, func(a, b string) bool {
		return canonicalOrder[a] < canonicalOrder[b]
	})
}

func orderModifiers(binding string, less func(a, b string) bool) string {
	parts := strings.Fields(strings.TrimSpace(binding))
	if len(parts) == 2 {
		return "Leader " + parts[1]
	}
	if len(parts) == 0 {
		return ""
	}
	tokens := strings.Split(parts[0], "+")
	if len(tokens) == 1 {
		return tokens[0]
	}
	key := tokens[len(tokens)-1]
	mods := append([]string(nil), tokens[:len(tokens)-1]...)
	sort.SliceStable(mods, func(i, j int) bool { return less(mods[i], mods[j]) })
	return strings.Join(append(mods, key), "+")
}
