import { eventToChord } from "../../../shortcuts/chord.ts";
import { liveResolvedKeymap } from "../../../contributions/frame-keymap.ts";
import { Prec } from "@codemirror/state";
import { EditorView, keymap, type KeyBinding } from "@codemirror/view";
import type { Extension } from "@codemirror/state";
import { invokeShortcutBinding, dispatchKeyboardEvent, pendingLeaderChord } from "../../../shortcuts/dispatcher.ts";
import { leaderIsPrefix } from "../../../shortcuts/keymap.ts";
import { shortcutPlatform } from "../../../shortcuts/platform.ts";
import type { TauriPlatform } from "../../../platform/runtime.ts";

/** Converts a canonical chord to editor keymap syntax. */
export function denChordToCodeMirror(chord: string): string {
  const parts = chord.split("+");
  const key = parts[parts.length - 1]!;
  const mods = parts.slice(0, -1);
  const keyOut = /^[A-Za-z]$/.test(key) ? key.toLowerCase() : key;
  return [...mods, keyOut].join("-");
}

export function buildEditorCommandBridge(
  platform: TauriPlatform = shortcutPlatform(),
  overrides?: Record<string, string> | null,
): Extension {
  const resolved = liveResolvedKeymap(platform, overrides);
  const defaults: KeyBinding[] = [];
  for (const binding of resolved.bindings) {
    if (binding.scope !== "files" || overrides?.[binding.bindingId]) continue;
    defaults.push({
      key: denChordToCodeMirror(binding.chord),
      run: view => invokeShortcutBinding(binding.bindingId, binding.chord, view.dom),
    });
  }
  const customBindings = resolved.bindings.filter(binding => overrides?.[binding.bindingId]);
  return [Prec.highest(EditorView.domEventHandlers({
    keydown(event, view) {
      if (event.isComposing || view.composing) return false;
      const chord = eventToChord(event, platform);
      if (pendingLeaderChord() || chord && leaderIsPrefix(resolved, chord)) return dispatchKeyboardEvent(event).handled;
      const binding = customBindings.find(candidate => candidate.chord === chord);
      return !!binding && invokeShortcutBinding(binding.bindingId, binding.chord, view.dom);
    },
  })), keymap.of(defaults)];
}
