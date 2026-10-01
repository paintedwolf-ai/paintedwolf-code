import type { ContributionFrameResponse } from "../api/types.ts";
import {
  bindingAllowedOnPlatform,
  defaultBindingAllowedOnPlatform,
} from "../shortcuts/binding-policy.ts";
import {
  resolveKeymap,
  type CommandScope,
  type KeymapDeclaration,
  type KeymapSource,
  type ResolvedKeymap,
} from "../shortcuts/keymap.ts";
import type { TauriPlatform } from "../platform/runtime.ts";
import { contributionFrame } from "./contribution-store.ts";
import { evaluateCondition } from "./conditions.ts";
import { liveShellFactLookup } from "./shell-facts.ts";

const SCOPES: readonly CommandScope[] = [
  "global",
  "files",
  "composer",
  "overlay",
];

function isScope(value: string): value is CommandScope {
  return (SCOPES as readonly string[]).includes(value);
}

/** Builds declaration-specific keymap input for one platform. */
export function frameKeymapSource(
  frame: ContributionFrameResponse | null,
  platform: TauriPlatform,
): KeymapSource {
  const activeDefaults = new Map<string, string[]>();
  if (frame) {
    for (const row of frame.binding_defaults) {
      if (row.platform !== platform || !row.active) continue;
      activeDefaults.set(row.active, [
        ...(activeDefaults.get(row.active) ?? []),
        row.chord,
      ]);
    }
  }

  const declarations: KeymapDeclaration[] = [];
  for (const binding of frame?.keybindings ?? []) {
    if (!isScope(binding.scope)) continue;
    if (!(platform in binding.bindings)) continue;
    declarations.push({
      id: binding.id,
      commandId: binding.command,
      scope: binding.scope,
      defaults: activeDefaults.get(binding.id) ?? [],
    });
  }
  return {
    declarations,
    bindingAllowed: (binding) => bindingAllowedOnPlatform(binding, platform),
    defaultBindingAllowed: (binding) =>
      defaultBindingAllowedOnPlatform(binding, platform),
  };
}

export function liveKeymapSource(platform: TauriPlatform): KeymapSource {
  return frameKeymapSource(contributionFrame(), platform);
}

const NO_FRAME = {};
const NO_OVERRIDES = {};
const resolvedKeymaps = new WeakMap<object, Map<TauriPlatform, WeakMap<object, ResolvedKeymap>>>();

/**
 * One resolution per frame, platform, and overrides object, shared by every reader.
 * Published frames and override maps are replaced on change, never mutated, so identity is
 * the whole key; an override map still being edited resolves through `resolveKeymap` instead.
 */
export function resolvedFrameKeymap(
  frame: ContributionFrameResponse | null,
  platform: TauriPlatform,
  overrides: Record<string, string> | null | undefined,
): ResolvedKeymap {
  const frameKey = frame ?? NO_FRAME;
  let byPlatform = resolvedKeymaps.get(frameKey);
  if (!byPlatform) resolvedKeymaps.set(frameKey, byPlatform = new Map());
  let byOverrides = byPlatform.get(platform);
  if (!byOverrides) byPlatform.set(platform, byOverrides = new WeakMap());
  const overridesKey = overrides ?? NO_OVERRIDES;
  let keymap = byOverrides.get(overridesKey);
  if (!keymap) {
    keymap = resolveKeymap(overrides, frameKeymapSource(frame, platform));
    byOverrides.set(overridesKey, keymap);
  }
  return keymap;
}

/** The resolved keymap of the live contribution frame. */
export function liveResolvedKeymap(
  platform: TauriPlatform,
  overrides: Record<string, string> | null | undefined,
): ResolvedKeymap {
  return resolvedFrameKeymap(contributionFrame(), platform, overrides);
}

/** Binding policy belongs to declaration identity. */
export function bindingAllowsInput(bindingId: string): boolean {
  return (
    contributionFrame()?.keybindings.find((row) => row.id === bindingId)
      ?.allow_in_input === true
  );
}

/** Whether one declaration's authored condition holds now. */
export function bindingActiveNow(bindingId: string): boolean {
  const binding = contributionFrame()?.keybindings.find(
    (row) => row.id === bindingId,
  );
  if (!binding) return false;
  if (!binding.when) return true;
  const lookup = liveShellFactLookup();
  return lookup ? evaluateCondition(binding.when, lookup) : false;
}
