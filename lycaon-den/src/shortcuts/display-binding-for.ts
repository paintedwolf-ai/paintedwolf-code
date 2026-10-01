import { liveResolvedKeymap } from "../contributions/frame-keymap.ts";
import { nativeCommandId } from "../contributions/dispatch.ts";
import { shortcutOverrides } from "../settings/system/shortcut-prefs.ts";
import {
  displayBinding,
  parseChord,
  type KeyboardEventLike,
} from "./chord.ts";
import { shortcutCommandMatchesEvent } from "./dispatcher.ts";
import {
  ariaModifierName,
  shortcutPlatform,
} from "./platform.ts";
import type { TauriPlatform } from "../platform/runtime.ts";

type BindingProjectionOptions = {
  platform?: TauriPlatform;
  overrides?: Record<string, string> | null;
};

/** Resolves display bindings for one command. */
function resolvedBindingsFor(
  commandId: string,
  opts?: BindingProjectionOptions,
): { platform: TauriPlatform; bindings: readonly string[] } {
  const platform = opts?.platform ?? shortcutPlatform();
  const overrides =
    opts && "overrides" in opts ? opts.overrides : shortcutOverrides();
  return {
    platform,
    bindings: liveResolvedKeymap(platform, overrides).byCommand.get(commandId) ?? [],
  };
}

/** Join canonical chords into platform display form (`? / ⌘/`). */
export function formatChordsDisplay(
  chords: readonly string[],
  platform: TauriPlatform = shortcutPlatform(),
): string {
  if (chords.length === 0) return "—";
  return chords.map((c) => displayBinding(c, platform)).join(" / ");
}

/** Formats every live binding for one command. */
export function displayBindingFor(
  commandId: string,
  opts?: BindingProjectionOptions,
): string {
  const { bindings, platform } = resolvedBindingsFor(commandId, opts);
  return formatChordsDisplay(bindings, platform);
}

function ariaChord(chord: string, platform: TauriPlatform): string | null {
  if (chord.includes(" ")) return null;
  const parsed = parseChord(chord);
  if (!parsed) return null;
  const tokens: string[] = [];
  if (parsed.mod) tokens.push(ariaModifierName(platform));
  if (parsed.ctrl) tokens.push("Control");
  if (parsed.alt) tokens.push("Alt");
  if (parsed.shift) tokens.push("Shift");
  if (parsed.super) tokens.push("Meta");
  tokens.push(parsed.key);
  return tokens.join("+");
}

/** WAI-ARIA spelling for a command's live one-step bindings. */
export function ariaKeyShortcutsFor(
  commandId: string,
  opts?: BindingProjectionOptions,
): string | undefined {
  const { bindings, platform } = resolvedBindingsFor(commandId, opts);
  const aria = bindings.flatMap((binding) => {
    const value = ariaChord(binding, platform);
    return value ? [value] : [];
  });
  return aria.length > 0 ? aria.join(" ") : undefined;
}

export function bindingForHandler(
  handlerId: string,
  opts?: BindingProjectionOptions,
): string {
  const commandId = nativeCommandId(handlerId);
  return commandId ? displayBindingFor(commandId, opts) : "—";
}

export function handlerMatchesEvent(
  handlerId: string,
  event: KeyboardEventLike & { target?: EventTarget | null },
): boolean {
  const commandId = nativeCommandId(handlerId);
  return commandId ? shortcutCommandMatchesEvent(commandId, event) : false;
}

export function ariaKeyShortcutsForHandler(
  handlerId: string,
  opts?: BindingProjectionOptions,
): string | undefined {
  const commandId = nativeCommandId(handlerId);
  return commandId ? ariaKeyShortcutsFor(commandId, opts) : undefined;
}
