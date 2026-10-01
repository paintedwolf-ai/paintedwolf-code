import {
  detectedPlatform,
  type TauriPlatform,
} from "../platform/runtime.ts";
import {
  ASSISTIVE_RESERVED_KEYS,
  OS_INTERCEPTED_CHORDS,
} from "./reserved-chords.generated.ts";

export const TAURI_PLATFORMS = ["macos", "windows", "linux"] as const satisfies readonly TauriPlatform[];

/** Test platform override. */
let injectedPlatform: TauriPlatform | null = null;

export function setShortcutPlatformForTests(
  platform: TauriPlatform | null,
): void {
  injectedPlatform = platform;
}

/** Returns the active shortcut platform. */
export function shortcutPlatform(): TauriPlatform {
  if (injectedPlatform) return injectedPlatform;
  return detectedPlatform() ?? "macos";
}

/** Primary app modifier: ⌘ on macOS, Ctrl elsewhere. */
export function modPressed(
  event: { metaKey: boolean; ctrlKey: boolean },
  platform: TauriPlatform = shortcutPlatform(),
): boolean {
  return platform === "macos" ? event.metaKey : event.ctrlKey;
}

/** Standard app chords and modifier families users may not rebind. */
export const reservedChords: Record<TauriPlatform, readonly string[]> = {
  macos: [
    "Mod+Q",
    "Mod+W",
    "Mod+H",
    "Mod+M",
    "Mod+`",
    "Mod+Alt+H",
    "Mod+Ctrl+F",
  ],
  windows: [
    "Alt+F4",
    "Alt+Escape",
    "Mod+Alt+*",
  ],
  linux: [
    "Mod+Alt+*",
    "Alt+F4",
  ],
};

/** Why a chord may not be bound, or null when it is free. */
export type ReservedReason = "system" | "os" | "assistive";

function chordTokens(chord: string): string[] {
  return chord.split("+").filter(Boolean);
}

/** Classifies a reserved chord. */
export function reservedReason(
  chord: string,
  platform: TauriPlatform = shortcutPlatform(),
): ReservedReason | null {
  const tokens = chordTokens(chord);
  if (tokens.length === 0) return null;
  if (ASSISTIVE_RESERVED_KEYS.includes(tokens[tokens.length - 1]!)) {
    return "assistive";
  }
  const held = new Set(tokens);
  const matches = (spec: string) => reservedSpecMatches(spec, chord, held);
  if (OS_INTERCEPTED_CHORDS[platform].some(matches)) return "os";
  if (reservedChords[platform].some(matches)) return "system";
  return null;
}

function reservedSpecMatches(
  spec: string,
  chord: string,
  held: ReadonlySet<string>,
): boolean {
  if (spec === chord) return true;
  if (!spec.endsWith("+*")) return false;
  return chordTokens(spec.slice(0, -2)).every((modifier) => held.has(modifier));
}
/** Faithful accelerator spellings for named keys. */
const ACCELERATOR_KEYS: Record<string, string> = {
  ",": "Comma",
  "/": "Slash",
  ".": "Period",
  ";": "Semicolon",
  "'": "Quote",
  "[": "BracketLeft",
  "]": "BracketRight",
  "\\": "Backslash",
  "-": "Minus",
  "=": "Equal",
  "`": "Backquote",
  Space: "Space",
  Enter: "Enter",
  Escape: "Escape",
  Tab: "Tab",
  Backspace: "Backspace",
  Delete: "Delete",
  Home: "Home",
  End: "End",
  PageUp: "PageUp",
  PageDown: "PageDown",
  ArrowUp: "Up",
  ArrowDown: "Down",
  ArrowLeft: "Left",
  ArrowRight: "Right",
};

const ACCELERATOR_MODIFIERS: Record<string, string> = {
  Mod: "CmdOrCtrl",
  Ctrl: "Ctrl",
  Alt: "Alt",
  Shift: "Shift",
  Super: "Super",
};

/** Returns an accelerator for a non-typing chord. */
export function acceleratorChord(chord: string): string | null {
  // Sequences stay in the in-window dispatcher.
  if (chord.includes(" ")) return null;
  const tokens = chordTokens(chord);
  if (tokens.length === 0) return null;
  // Typing and Alt-only chords stay in the in-window dispatcher.
  const held = new Set(tokens.slice(0, -1));
  if (!held.has("Mod") && !held.has("Ctrl") && !held.has("Super")) return null;
  const key = tokens[tokens.length - 1]!;

  let acceleratorKey: string | null = null;
  if (/^[A-Z0-9]$/.test(key)) acceleratorKey = key;
  else if (/^F\d{1,2}$/.test(key)) acceleratorKey = key;
  else acceleratorKey = ACCELERATOR_KEYS[key] ?? null;
  if (!acceleratorKey) return null;

  const modifiers: string[] = [];
  for (const token of tokens.slice(0, -1)) {
    const mapped = ACCELERATOR_MODIFIERS[token];
    if (!mapped) return null;
    modifiers.push(mapped);
  }
  return [...modifiers, acceleratorKey].join("+");
}

const MAC_GLYPHS: Record<string, string> = {
  Mod: "⌘",
  Ctrl: "⌃",
  Alt: "⌥",
  Shift: "⇧",
  Super: "⌘",
};

/** Display glyphs for directional keys. */
const KEY_GLYPHS: Record<string, string> = {
  ArrowUp: "↑",
  ArrowDown: "↓",
  ArrowLeft: "←",
  ArrowRight: "→",
};

function displayKeyToken(key: string): string {
  return KEY_GLYPHS[key] ?? key;
}

/** Returns display-only keycap tokens. */
export function displayChordParts(
  chord: string,
  platform: TauriPlatform = shortcutPlatform(),
): string[] {
  const parts = chordTokens(chord);
  if (parts.length === 0) return [chord];
  if (platform === "macos") {
    return [
      ...parts.slice(0, -1).map((p) => MAC_GLYPHS[p] ?? p),
      displayKeyToken(parts[parts.length - 1]!),
    ];
  }
  return parts.map((p, i) =>
    i === parts.length - 1 ? displayKeyToken(p) : p === "Mod" ? "Ctrl" : p,
  );
}

/** Returns the accessible primary-modifier name. */
export function ariaModifierName(
  platform: TauriPlatform = shortcutPlatform(),
): string {
  return platform === "macos" ? "Meta" : "Control";
}

/** Returns the display-only chord spelling. */
export function displayChord(
  chord: string,
  platform: TauriPlatform = shortcutPlatform(),
): string {
  const parts = displayChordParts(chord, platform);
  if (parts.length === 0) return chord;
  if (platform === "macos") return parts.join("");
  return parts.join("+");
}
