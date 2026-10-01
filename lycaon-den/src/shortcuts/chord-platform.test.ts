import { afterEach, describe, expect, it } from "vitest";
import {
  chordBypassesTextInput,
  bindingIsProducible,
  displayBinding,
  eventToChord,
  normalizeChord,
  parseChord,
  serializeChord,
  type KeyboardEventLike,
} from "./chord.ts";
import {
  acceleratorChord,
  displayChord,
  displayChordParts,
  reservedChords,
  reservedReason,
  setShortcutPlatformForTests,
  TAURI_PLATFORMS,
} from "./platform.ts";
import type { TauriPlatform } from "../platform/runtime.ts";
import { STOCK_FRAME } from "../contributions/stock-frame.generated.ts";
import { OS_INTERCEPTED_CHORDS } from "./reserved-chords.generated.ts";
import { DEFAULT_NAV_LEADER_CHORD } from "./keymap.ts";
import {
  bindingAllowedOnPlatform,
  defaultBindingAllowedOnPlatform,
} from "./binding-policy.ts";

afterEach(() => {
  setShortcutPlatformForTests(null);
});

function evt(partial: Partial<KeyboardEventLike> & Pick<KeyboardEventLike, "key" | "code">): KeyboardEventLike {
  return {
    metaKey: false,
    ctrlKey: false,
    altKey: false,
    shiftKey: false,
    ...partial,
  };
}

describe("platform tables exhaustive", () => {
  it("reservedChords keys === every TauriPlatform", () => {
    expect(Object.keys(reservedChords).sort()).toEqual(
      [...TAURI_PLATFORMS].sort(),
    );
    expect(Object.keys(OS_INTERCEPTED_CHORDS).sort()).toEqual(
      [...TAURI_PLATFORMS].sort(),
    );
  });

  it("reservedReason answers on every TauriPlatform", () => {
    for (const platform of TAURI_PLATFORMS) {
      expect(reservedReason("Mod+Shift+F7", platform)).toBeNull();
    }
  });
});

describe("Mod resolution (injected platform)", () => {
  it("⌘K on macOS → Mod+K", () => {
    setShortcutPlatformForTests("macos");
    expect(
      eventToChord(evt({ key: "k", code: "KeyK", metaKey: true }), "macos"),
    ).toBe("Mod+K");
  });

  it("Ctrl+K on linux/windows → Mod+K", () => {
    for (const platform of ["linux", "windows"] as const) {
      expect(
        eventToChord(evt({ key: "k", code: "KeyK", ctrlKey: true }), platform),
      ).toBe("Mod+K");
    }
  });

  it("Ctrl+K on macOS is literal Ctrl+K (not Mod)", () => {
    expect(
      eventToChord(evt({ key: "k", code: "KeyK", ctrlKey: true }), "macos"),
    ).toBe("Ctrl+K");
  });
});

describe("code-based alphanumeric matching", () => {
  it("physical KeyK matches Mod+K even when produced key is non-US", () => {
    expect(
      eventToChord(
        evt({ key: "ç", code: "KeyK", metaKey: true }),
        "macos",
      ),
    ).toBe("Mod+K");
    expect(
      eventToChord(
        evt({ key: "ç", code: "KeyK", ctrlKey: true }),
        "linux",
      ),
    ).toBe("Mod+K");
  });

  it("Digit1 uses code, not layout glyph", () => {
    expect(
      eventToChord(
        evt({ key: "&", code: "Digit1", ctrlKey: true }),
        "windows",
      ),
    ).toBe("Mod+1");
  });
});

describe("chord serialize/parse", () => {
  it("round-trips canonical forms", () => {
    for (const chord of [
      "Mod+K",
      "Mod+Shift+P",
      "Shift+Enter",
      "Escape",
      "?",
      "Mod+/",
      "Mod+,",
      "Mod+Alt+Delete",
      "Super+E",
    ]) {
      expect(normalizeChord(chord)).toBe(chord);
      const parts = parseChord(chord);
      expect(parts).not.toBeNull();
      expect(serializeChord(parts!)).toBe(chord);
    }
  });

  it("rejects empty / unknown tokens", () => {
    expect(parseChord("")).toBeNull();
    expect(parseChord("Mod+Foo")).toBeNull();
    expect(normalizeChord("Mod+Foo")).toBeNull();
    expect(parseChord("Meta+E")).toBeNull();
    expect(parseChord("Cmd+E")).toBeNull();
  });

  it("canonicalizes modifier order", () => {
    expect(normalizeChord("Shift+Mod+K")).toBe("Mod+Shift+K");
  });
});

describe("reservedReason", () => {
  it("exact macos reserved chords", () => {
    expect(reservedReason("Mod+Q", "macos")).toBe("system");
    expect(reservedReason("Mod+K", "macos")).toBeNull();
  });

  it("Mod+Alt+* on linux and windows", () => {
    for (const platform of ["linux", "windows"] as const) {
      expect(reservedReason("Mod+Alt+A", platform)).toBe("system");
      expect(reservedReason("Mod+Alt+Delete", platform)).toBe("os");
      expect(reservedReason("Mod+K", platform)).toBeNull();
    }
  });

  it("Super+* on linux and windows", () => {
    expect(reservedReason("Super+A", "linux")).toBe("os");
    expect(reservedReason("Super+D", "windows")).toBe("os");
  });

  it("macOS consumes Spaces, Dock, and screenshot chords before the app", () => {
    for (const chord of [
      "Mod+Alt+D",
      "Ctrl+ArrowLeft",
      "Ctrl+ArrowUp",
      "Mod+Shift+4",
      "Mod+Space",
    ]) {
      expect(reservedReason(chord, "macos")).toBe("os");
      expect(defaultBindingAllowedOnPlatform(chord, "macos")).toBe(false);
    }
  });

  it("ships no default the operating system would swallow", () => {
    for (const binding of STOCK_FRAME.keybindings) {
      for (const [platform, chords] of Object.entries(binding.bindings)) {
        for (const declared of chords) {
          const chord = declared.startsWith("Leader ")
            ? `${DEFAULT_NAV_LEADER_CHORD} ${declared.slice("Leader ".length)}`
            : declared;
          expect(
            defaultBindingAllowedOnPlatform(chord, platform as TauriPlatform),
            `${binding.id} ${platform} ${declared}`,
          ).toBe(true);
        }
      }
    }
  });

  it("rejects AltGr-equivalent Mod+Alt through the complete binding policy", () => {
    for (const platform of ["linux", "windows"] as const) {
      expect(bindingAllowedOnPlatform("Mod+Alt+A", platform)).toBe(false);
      expect(bindingAllowedOnPlatform("Mod+Alt+A D", platform)).toBe(false);
    }
    expect(bindingAllowedOnPlatform("Mod+Alt+A", "macos")).toBe(true);
  });

  it("lets trusted stock defaults use system chords but never assistive keys", () => {
    expect(defaultBindingAllowedOnPlatform("Mod+W", "macos")).toBe(true);
    expect(defaultBindingAllowedOnPlatform("Insert", "macos")).toBe(false);
    expect(defaultBindingAllowedOnPlatform("Mod+; Insert", "macos")).toBe(
      false,
    );
  });

  it("screen-reader keys read as assistive, not as a system clash", () => {
    expect(reservedReason("CapsLock", "macos")).toBe("assistive");
    expect(reservedReason("Mod+Insert", "windows")).toBe("assistive");
  });
});

describe("native accelerator safety", () => {
  it("only exposes modifier chords whose native path can be re-gated", () => {
    expect(acceleratorChord("Mod+K")).toBe("CmdOrCtrl+K");
    expect(acceleratorChord("Ctrl+K")).toBe("Ctrl+K");
    expect(acceleratorChord("Super+K")).toBe("Super+K");
    expect(acceleratorChord("Alt+K")).toBeNull();
    expect(acceleratorChord("Enter")).toBeNull();
    expect(acceleratorChord("Mod+; C")).toBeNull();
  });
});

describe("display glyphs", () => {
  it("macOS uses symbols without plus for Mod+letter", () => {
    expect(displayChord("Mod+K", "macos")).toBe("⌘K");
    expect(displayChord("Mod+Shift+P", "macos")).toBe("⌘⇧P");
    expect(displayBinding("Mod+,", "macos")).toBe("⌘,");
  });

  it("linux/windows spell Ctrl+", () => {
    for (const platform of ["linux", "windows"] as const) {
      expect(displayChord("Mod+K", platform)).toBe("Ctrl+K");
      expect(displayChord("Mod+Shift+P", platform)).toBe("Ctrl+Shift+P");
    }
  });

  it("displayChordParts splits tokens for keycap UI", () => {
    expect(displayChordParts("Mod+K", "macos")).toEqual(["⌘", "K"]);
    expect(displayChordParts("Mod+Shift+P", "macos")).toEqual(["⌘", "⇧", "P"]);
    expect(displayChordParts("Mod+K", "linux")).toEqual(["Ctrl", "K"]);
    expect(displayChordParts("Mod+Shift+P", "windows")).toEqual([
      "Ctrl",
      "Shift",
      "P",
    ]);
  });

  it("arrow keys display as glyphs on every platform", () => {
    expect(displayChord("Alt+ArrowDown", "macos")).toBe("⌥↓");
    expect(displayChord("Alt+ArrowUp", "macos")).toBe("⌥↑");
    expect(displayChord("Alt+ArrowDown", "linux")).toBe("Alt+↓");
    expect(displayChordParts("Alt+ArrowRight", "windows")).toEqual(["Alt", "→"]);
  });
});

describe("bindingIsProducible", () => {
  it("accepts a literal Ctrl on macOS and refuses it elsewhere", () => {
    expect(bindingIsProducible("Ctrl+Tab", "macos")).toBe(true);
    expect(bindingIsProducible("Ctrl+Tab", "windows")).toBe(false);
    expect(bindingIsProducible("Ctrl+Tab", "linux")).toBe(false);
    expect(bindingIsProducible("Mod+Ctrl+G", "macos")).toBe(true);
    expect(bindingIsProducible("Mod+Ctrl+G", "linux")).toBe(false);
  });

  it("accepts Super off macOS and refuses it on macOS", () => {
    expect(bindingIsProducible("Super+A", "linux")).toBe(true);
    expect(bindingIsProducible("Super+A", "macos")).toBe(false);
  });

  it("round-trips the ordinary shapes on every platform", () => {
    for (const platform of TAURI_PLATFORMS) {
      for (const chord of [
        "Mod+K",
        "Mod+Shift+P",
        "Shift+Enter",
        "Escape",
        "?",
        "Mod+/",
        "Alt+ArrowUp",
        "F12",
        "Mod+Shift+]",
        "Mod+Alt+\\",
      ]) {
        expect(
          bindingIsProducible(chord, platform),
          `${platform} ${chord}`,
        ).toBe(true);
      }
    }
  });
});

describe("chordBypassesTextInput", () => {
  it("macOS Option types a character, so an Alt chord stays with the field", () => {
    expect(chordBypassesTextInput("Alt+C", "macos")).toBe(false);
    expect(chordBypassesTextInput("Alt+C", "windows")).toBe(true);
    expect(chordBypassesTextInput("Alt+C", "linux")).toBe(true);
  });

  it("Mod and Ctrl chords bypass on every platform", () => {
    for (const platform of TAURI_PLATFORMS) {
      expect(chordBypassesTextInput("Mod+K", platform)).toBe(true);
      expect(chordBypassesTextInput("Mod+Alt+U", platform)).toBe(true);
      expect(chordBypassesTextInput("Ctrl+Tab", platform)).toBe(true);
    }
  });

  it("bare and Shift-only chords never bypass", () => {
    for (const platform of TAURI_PLATFORMS) {
      expect(chordBypassesTextInput("?", platform)).toBe(false);
      expect(chordBypassesTextInput("Shift+Enter", platform)).toBe(false);
      expect(chordBypassesTextInput("Enter", platform)).toBe(false);
    }
  });
});

describe("named keys and help chords", () => {
  it("Escape / Enter / Shift+Enter", () => {
    expect(eventToChord(evt({ key: "Escape", code: "Escape" }), "macos")).toBe(
      "Escape",
    );
    expect(eventToChord(evt({ key: "Enter", code: "Enter" }), "linux")).toBe(
      "Enter",
    );
    expect(
      eventToChord(
        evt({ key: "Enter", code: "Enter", shiftKey: true }),
        "windows",
      ),
    ).toBe("Shift+Enter");
  });

  it("? ignores producing Shift and Mod+/ remains layout-safe", () => {
    expect(
      eventToChord(
        evt({ key: "?", code: "Slash", shiftKey: true }),
        "macos",
      ),
    ).toBe("?");
    expect(
      eventToChord(
        evt({ key: "/", code: "Slash", metaKey: true }),
        "macos",
      ),
    ).toBe("Mod+/");
  });

  it("Mod+. and Alt+Arrow map on every platform", () => {
    expect(
      eventToChord(evt({ key: ".", code: "Period", metaKey: true }), "macos"),
    ).toBe("Mod+.");
    expect(
      eventToChord(evt({ key: ".", code: "Period", ctrlKey: true }), "linux"),
    ).toBe("Mod+.");
    expect(
      eventToChord(
        evt({ key: "ArrowUp", code: "ArrowUp", altKey: true }),
        "windows",
      ),
    ).toBe("Alt+ArrowUp");
    expect(
      eventToChord(
        evt({ key: "ArrowDown", code: "ArrowDown", altKey: true }),
        "macos",
      ),
    ).toBe("Alt+ArrowDown");
  });
});
