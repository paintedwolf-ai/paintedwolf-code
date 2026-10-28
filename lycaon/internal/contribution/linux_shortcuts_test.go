package contribution

import "testing"

func TestLinuxDesktopAndToolkitReservationsRejectStockAndPackDefaults(t *testing.T) {
	for _, chord := range []string{
		"Mod+Alt+A", "Mod+Alt+Shift+ArrowLeft", "Mod+Alt+Shift+ArrowRight",
		"Mod+Alt+Shift+ArrowUp", "Mod+Alt+Shift+ArrowDown", "Mod+.", "Mod+;",
	} {
		for _, stock := range []bool{false, true} {
			binding := &Keybinding{Command: "acme/a:c", Scope: "global",
				Bindings: map[string][]string{"linux": {chord}}}
			if err := validateKeybinding(binding, stock); err == nil {
				t.Fatalf("Linux %s stock=%t bypassed the desktop/toolkit reservation", chord, stock)
			}
		}
	}
	for _, chord := range []string{"Mod+A", "Mod+Shift+PageUp", "Mod+Shift+PageDown", "Shift+F5", "Mod+Shift+;"} {
		binding := &Keybinding{Command: "acme/a:c", Scope: "global",
			Bindings: map[string][]string{"linux": {chord}}}
		if err := validateKeybinding(binding, true); err != nil {
			t.Fatalf("replacement Linux default %s: %v", chord, err)
		}
	}
	for _, platform := range []string{"macos", "windows"} {
		binding := &Keybinding{Command: "acme/a:c", Scope: "global",
			Bindings: map[string][]string{platform: {"Mod+.", "Mod+;", "Mod+Alt+A", "Mod+Alt+Shift+ArrowLeft"}}}
		if err := validateKeybinding(binding, true); err != nil {
			t.Fatalf("existing %s defaults changed: %v", platform, err)
		}
	}
}
