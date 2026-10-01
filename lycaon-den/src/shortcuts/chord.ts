import {
  displayChord,
  displayChordParts,
  modPressed,
  shortcutPlatform,
} from "./platform.ts";
import type { TauriPlatform } from "../platform/runtime.ts";

const MODIFIER_ORDER = ["Mod", "Ctrl", "Alt", "Shift", "Super"] as const;

const NAMED_KEYS = new Set([
  "Enter",
  "Escape",
  "Tab",
  "Backspace",
  "Delete",
  "Space",
  "ArrowUp",
  "ArrowDown",
  "ArrowLeft",
  "ArrowRight",
  "Home",
  "End",
  "PageUp",
  "PageDown",
  // Assistive modifiers remain parseable for clear validation errors.
  "CapsLock",
  "Insert",
  "ScrollLock",
]);

/** Punctuation token → physical `code`. Inverse of the `keyFromCode` arms below. */
const PUNCT_CODES: Record<string, string> = {
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
};

const PUNCT_KEYS = new Set(Object.keys(PUNCT_CODES));

export type ChordParts = {
  mod?: boolean;
  ctrl?: boolean;
  alt?: boolean;
  shift?: boolean;
  super?: boolean;
  key: string;
};

export type KeyboardEventLike = {
  key: string;
  code: string;
  metaKey: boolean;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
};

const PUNCT_BY_CODE: Record<string, string> = Object.fromEntries(
  Object.entries(PUNCT_CODES).map(([key, code]) => [code, key]),
);

function keyFromCode(code: string): string | null {
  if (code.startsWith("Key") && code.length === 4) {
    return code.slice(3).toUpperCase();
  }
  if (code.startsWith("Digit") && code.length === 6) {
    return code.slice(5);
  }
  if (code === "Space") return "Space";
  return PUNCT_BY_CODE[code] ?? null;
}

function normalizeKeyToken(raw: string): string | null {
  if (!raw) return null;
  if (raw === " ") return "Space";
  if (NAMED_KEYS.has(raw)) return raw;
  if (PUNCT_KEYS.has(raw)) return raw;
  if (raw === "?") return "?";
  if (raw.length === 1) {
    if (/[a-zA-Z]/.test(raw)) return raw.toUpperCase();
    if (/[0-9]/.test(raw)) return raw;
  }
  if (/^F\d{1,2}$/.test(raw)) return raw;
  return null;
}

/** Serialize parts to canonical chord string (fixed modifier order). */
export function serializeChord(parts: ChordParts): string {
  const tokens: string[] = [];
  if (parts.mod) tokens.push("Mod");
  if (parts.ctrl) tokens.push("Ctrl");
  if (parts.alt) tokens.push("Alt");
  if (parts.shift) tokens.push("Shift");
  if (parts.super) tokens.push("Super");
  tokens.push(parts.key);
  return tokens.join("+");
}

/** Parse a canonical chord string. Returns null on unknown / empty. */
export function parseChord(canonical: string): ChordParts | null {
  if (!canonical || typeof canonical !== "string") return null;
  const tokens = canonical.split("+").filter(Boolean);
  if (tokens.length === 0) return null;

  const parts: ChordParts = { key: "" };
  const keyToken = tokens[tokens.length - 1];
  if (!keyToken) return null;
  const key = normalizeKeyToken(keyToken);
  if (!key) return null;
  parts.key = key;

  const seen = new Set<string>();
  for (const token of tokens.slice(0, -1)) {
    if (seen.has(token)) return null;
    seen.add(token);
    switch (token) {
      case "Mod":
        parts.mod = true;
        break;
      case "Ctrl":
        parts.ctrl = true;
        break;
      case "Alt":
        parts.alt = true;
        break;
      case "Shift":
        parts.shift = true;
        break;
      case "Super":
        parts.super = true;
        break;
      default:
        return null;
    }
  }
  return parts;
}

/** Normalize any chord string to canonical form, or null if invalid. */
export function normalizeChord(canonical: string): string | null {
  const parts = parseChord(canonical);
  if (!parts) return null;
  return serializeChord(parts);
}

/** Returns whether a chord has a non-Shift modifier. */
export function chordHasNonShiftModifier(canonical: string): boolean {
  const parts = parseChord(canonical);
  if (!parts) return false;
  return Boolean(parts.mod || parts.ctrl || parts.alt || parts.super);
}

/** Returns whether the held modifiers prevent text entry. */
export function chordBypassesTextInput(
  canonical: string,
  platform: TauriPlatform = shortcutPlatform(),
): boolean {
  const parts = parseChord(canonical);
  if (!parts) return false;
  if (parts.mod || parts.ctrl || parts.super) return true;
  if (parts.alt) return platform !== "macos";
  return false;
}

/** Maps a keyboard event to a canonical chord. */
export function eventToChord(
  event: KeyboardEventLike,
  platform: TauriPlatform = shortcutPlatform(),
): string | null {
  const isMod = modPressed(event, platform);
  const parts: ChordParts = { key: "" };

  if (platform === "macos") {
    if (isMod) parts.mod = true;
    if (event.ctrlKey) parts.ctrl = true;
  } else {
    if (isMod) parts.mod = true;
    // Ctrl is Mod off macOS; the separate OS key is Super.
    if (event.metaKey) parts.super = true;
  }
  if (event.altKey) parts.alt = true;

  // Preserve the produced question-mark key.
  if (event.key === "?") {
    parts.key = "?";
    return serializeChord(parts);
  }

  if (event.shiftKey) parts.shift = true;

  const fromCode = keyFromCode(event.code);
  if (fromCode && /^[A-Z0-9]$/.test(fromCode)) {
    parts.key = fromCode;
    return serializeChord(parts);
  }

  // Physical codes keep punctuation stable across layouts.
  if (fromCode && (PUNCT_KEYS.has(fromCode) || fromCode === "Space")) {
    parts.key = fromCode;
    return serializeChord(parts);
  }

  const fromKey = normalizeKeyToken(event.key === " " ? "Space" : event.key);
  if (!fromKey) return null;
  if (MODIFIER_ORDER.includes(fromKey as (typeof MODIFIER_ORDER)[number])) {
    return null;
  }
  parts.key = fromKey;
  return serializeChord(parts);
}

/** Builds a synthetic event key pair for one token. */
function eventKeyFor(key: string): { code: string; key: string } | null {
  if (/^[A-Z]$/.test(key)) return { code: `Key${key}`, key: key.toLowerCase() };
  if (/^[0-9]$/.test(key)) return { code: `Digit${key}`, key };
  const punctCode = PUNCT_CODES[key];
  if (punctCode) return { code: punctCode, key };
  if (key === "Space") return { code: "Space", key: " " };
  if (key === "?") return { code: "Slash", key: "?" };
  if (NAMED_KEYS.has(key) || /^F\d{1,2}$/.test(key)) return { code: key, key };
  return null;
}

/** Checks whether a platform can emit a canonical chord. */
function chordIsProducible(
  canonical: string,
  platform: TauriPlatform = shortcutPlatform(),
): boolean {
  const parts = parseChord(canonical);
  if (!parts) return false;
  const named = eventKeyFor(parts.key);
  if (!named) return false;
  const mac = platform === "macos";
  const event: KeyboardEventLike = {
    key: named.key,
    code: named.code,
    metaKey: Boolean((parts.mod && mac) || parts.super),
    ctrlKey: Boolean((parts.mod && !mac) || parts.ctrl),
    altKey: Boolean(parts.alt),
    shiftKey: Boolean(parts.shift || parts.key === "?"),
  };
  return eventToChord(event, platform) === serializeChord(parts);
}

/** Checks whether a platform can emit every binding step. */
export function bindingIsProducible(
  canonical: string,
  platform: TauriPlatform = shortcutPlatform(),
): boolean {
  const parsed = parseBinding(canonical);
  if (!parsed) return false;
  if (parsed.kind === "chord") return chordIsProducible(parsed.chord, platform);
  return (
    chordIsProducible(parsed.leader, platform) &&
    chordIsProducible(parsed.secondKey, platform)
  );
}

export type SequenceParts = {
  leader: string;
  secondKey: string;
};

export type ParsedBinding =
  | { kind: "chord"; chord: string }
  | { kind: "sequence"; leader: string; secondKey: string };

/** True when the canonical string is a two-step sequence (contains a space). */
export function isSequenceBinding(canonical: string): boolean {
  return typeof canonical === "string" && canonical.includes(" ");
}

/** Parses one leader chord and one bare key. */
export function parseSequence(canonical: string): SequenceParts | null {
  if (!canonical || typeof canonical !== "string") return null;
  if (canonical !== canonical.trim()) return null;
  const steps = canonical.split(" ");
  if (steps.length !== 2) return null;
  const [leaderRaw, secondRaw] = steps;
  if (!leaderRaw || !secondRaw) return null;
  if (secondRaw.includes("+")) return null;
  const leader = normalizeChord(leaderRaw);
  if (!leader || !chordHasNonShiftModifier(leader)) return null;
  const secondKey = normalizeKeyToken(secondRaw);
  if (!secondKey) return null;
  if (MODIFIER_ORDER.includes(secondKey as (typeof MODIFIER_ORDER)[number])) {
    return null;
  }
  return { leader, secondKey };
}

/** Serialize a validated leader chord + bare second key. */
export function serializeSequence(
  leader: string,
  secondKey: string,
): string | null {
  const normalizedLeader = normalizeChord(leader);
  if (!normalizedLeader || !chordHasNonShiftModifier(normalizedLeader)) {
    return null;
  }
  const key = normalizeKeyToken(secondKey);
  if (!key) return null;
  if (MODIFIER_ORDER.includes(key as (typeof MODIFIER_ORDER)[number])) {
    return null;
  }
  return `${normalizedLeader} ${key}`;
}

/** Parse one chord or exactly one two-step sequence. */
export function parseBinding(canonical: string): ParsedBinding | null {
  if (!canonical || typeof canonical !== "string") return null;
  if (canonical.includes(" ")) {
    const seq = parseSequence(canonical);
    if (!seq) return null;
    return { kind: "sequence", leader: seq.leader, secondKey: seq.secondKey };
  }
  const chord = normalizeChord(canonical);
  if (!chord) return null;
  return { kind: "chord", chord };
}

/** Normalize any binding string to canonical form, or null if invalid. */
export function normalizeBinding(canonical: string): string | null {
  const parsed = parseBinding(canonical);
  if (!parsed) return null;
  if (parsed.kind === "chord") return parsed.chord;
  return serializeSequence(parsed.leader, parsed.secondKey);
}

/** Maps an event to a bare sequence key. */
export function eventToBareKey(event: KeyboardEventLike): string | null {
  if (event.metaKey || event.ctrlKey || event.altKey) return null;
  // Produced punctuation takes precedence over its physical key.
  if (event.key === "?") return "?";
  // Shifted letters are not bare sequence keys.
  const fromCode = keyFromCode(event.code);
  if (fromCode && /^[A-Z0-9]$/.test(fromCode)) {
    if (event.shiftKey) return null;
    return fromCode;
  }
  if (fromCode && (PUNCT_KEYS.has(fromCode) || fromCode === "Space")) {
    if (event.shiftKey) return null;
    return fromCode;
  }
  const fromKey = normalizeKeyToken(event.key === " " ? "Space" : event.key);
  if (!fromKey) return null;
  if (MODIFIER_ORDER.includes(fromKey as (typeof MODIFIER_ORDER)[number])) {
    return null;
  }
  if (event.shiftKey && /^[A-Z]$/.test(fromKey)) return null;
  return fromKey;
}

/** Platform display form — never stored. Sequences render as "leader then second". */
export function displayBinding(
  canonical: string,
  platform: TauriPlatform = shortcutPlatform(),
): string {
  const parsed = parseBinding(canonical);
  if (!parsed) return canonical;
  if (parsed.kind === "chord") {
    return displayChord(parsed.chord, platform);
  }
  const leader = displayChord(parsed.leader, platform);
  const second = displayChord(parsed.secondKey, platform);
  return `${leader} then ${second}`;
}

/** Keycap part groups for a binding — one group per step. */
export function displayBindingPartGroups(
  canonical: string,
  platform: TauriPlatform = shortcutPlatform(),
): string[][] {
  const parsed = parseBinding(canonical);
  if (!parsed) return [[canonical]];
  if (parsed.kind === "chord") {
    return [displayChordParts(parsed.chord, platform)];
  }
  return [
    displayChordParts(parsed.leader, platform),
    displayChordParts(parsed.secondKey, platform),
  ];
}
