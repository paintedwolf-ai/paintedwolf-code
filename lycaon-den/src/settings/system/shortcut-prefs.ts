import { createSignal } from "solid-js";
import type { DenShortcutPrefs } from "../../../shared/app-state-types.ts";
import { contributionFrame } from "../../contributions/contribution-store.ts";
import { liveKeymapSource } from "../../contributions/frame-keymap.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";
import { bindingPolicyFailure } from "../../shortcuts/binding-policy.ts";
import {
  chordHasNonShiftModifier,
  displayBinding,
  normalizeBinding,
  normalizeChord,
  parseBinding,
} from "../../shortcuts/chord.ts";
import {
  DEFAULT_NAV_LEADER_CHORD,
  detectConflicts,
  detectSequenceConflicts,
  leaderCollidesWithCatalog,
  leaderShadowedBindings,
  NAV_LEADER_ID,
  rebindLeaderOverrides,
  resolveKeymap,
  type KeymapSource,
  type ResolvedKeymap,
} from "../../shortcuts/keymap.ts";
import { setShortcutOverrides } from "../../shortcuts/dispatcher.ts";
import { shortcutPlatform } from "../../shortcuts/platform.ts";

const [overrides, setOverrides] = createSignal<Record<string, string>>({});

type ShortcutRefusal = { ok: false; reason: string };
export type ShortcutSaveResult = { ok: true } | ShortcutRefusal;

/** Keybinding declaration ID (or nav.leader) → canonical binding. */
export function shortcutOverrides(): Record<string, string> {
  return overrides();
}

function applyOverrides(next: Record<string, string>): void {
  setOverrides(next);
  setShortcutOverrides(Object.keys(next).length > 0 ? next : null);
}

function refuse(reason: string): ShortcutRefusal {
  return { ok: false, reason };
}

function commandIdForBinding(bindingId: string): string | null {
  return (
    contributionFrame()?.keybindings.find((row) => row.id === bindingId)
      ?.command ?? null
  );
}

function labelForBinding(bindingId: string): string {
  const frame = contributionFrame();
  const commandId = commandIdForBinding(bindingId);
  return (
    frame?.commands.find((row) => row.id === commandId)?.title ??
    commandId ??
    bindingId
  );
}

function policyRefusal(binding: string): ShortcutRefusal | null {
  const platform = shortcutPlatform();
  const failure = bindingPolicyFailure(binding, platform);
  if (!failure) return null;
  if (failure.kind === "syntax") {
    return refuse("That key combination can’t be used as a shortcut.");
  }
  if (failure.kind === "unproducible") {
    return refuse(
      `${displayBinding(binding, platform)} can’t be produced on this keyboard.`,
    );
  }
  const shown = displayBinding(failure.chord, platform);
  const refusals: Record<typeof failure.reason, string> = {
    assistive: `${shown} is reserved for screen readers and can’t be used.`,
    os: `${shown} is handled by the operating system before any app sees it.`,
    system: `${shown} is reserved by the system and can’t be used.`,
  };
  return refuse(refusals[failure.reason]);
}

type LeaderVerdict = { ok: true; chord: string } | ShortcutRefusal;

function validateLeader(binding: string, source: KeymapSource): LeaderVerdict {
  const chord = normalizeChord(binding);
  if (!chord) {
    return refuse("That key combination can’t be used as the leader key.");
  }
  if (!chordHasNonShiftModifier(chord)) {
    return refuse(
      "The leader needs Command, Control, Option, or another non-Shift modifier.",
    );
  }
  const policy = policyRefusal(chord);
  if (policy) return policy;
  if (leaderCollidesWithCatalog(chord, source)) {
    return refuse(
      `${displayBinding(chord, shortcutPlatform())} is already used by another shortcut and can’t be the leader.`,
    );
  }
  return { ok: true, chord };
}

function conflictFor(
  bindingId: string,
  keymap: ResolvedKeymap,
): string | null | undefined {
  const groups = [...detectConflicts(keymap), ...detectSequenceConflicts(keymap)];
  const hit = groups.find((group) => group.bindingIds.includes(bindingId));
  if (!hit) return undefined;
  return hit.bindingIds.find((id) => id !== bindingId) ?? null;
}

function validateDeclarationBinding(
  bindingId: string,
  binding: string,
  next: Record<string, string>,
  source: KeymapSource,
): ShortcutSaveResult {
  if (!source.declarations.some((row) => row.id === bindingId)) {
    return refuse("That shortcut can’t be changed.");
  }
  const policy = policyRefusal(binding);
  if (policy) return policy;
  const parsed = parseBinding(binding);
  if (!parsed) {
    return refuse("That key combination can’t be used as a shortcut.");
  }
  const leaderChord = resolveKeymap(overrides(), source).leaderChord;
  if (parsed.kind === "chord" && parsed.chord === leaderChord) {
    return refuse(
      `${displayBinding(parsed.chord, shortcutPlatform())} is the leader key. Change the leader first, or pick another combination.`,
    );
  }
  if (parsed.kind === "sequence" && parsed.leader !== leaderChord) {
    return refuse(
      `A sequence has to start with the leader key, ${displayBinding(leaderChord, shortcutPlatform())}.`,
    );
  }

  const other = conflictFor(bindingId, resolveKeymap(next, source));
  if (other !== undefined) {
    return refuse(
      other
        ? `${displayBinding(binding, shortcutPlatform())} is already used by ${labelForBinding(other)}.`
        : `${displayBinding(binding, shortcutPlatform())} is already used by another shortcut.`,
    );
  }
  return { ok: true };
}

export function resolveShortcutOverrides(
  prefs?: DenShortcutPrefs,
): Record<string, string> {
  const raw = prefs?.overrides;
  if (!raw) return {};
  const platform = shortcutPlatform();
  const source = liveKeymapSource(platform);
  const next: Record<string, string> = {};

  const leaderRaw = raw[NAV_LEADER_ID];
  if (typeof leaderRaw === "string") {
    const verdict = validateLeader(leaderRaw, source);
    if (verdict.ok) next[NAV_LEADER_ID] = verdict.chord;
  }
  const leaderChord = resolveKeymap(next, source).leaderChord;

  for (const declaration of source.declarations) {
    const rawBinding = raw[declaration.id];
    if (typeof rawBinding !== "string") continue;
    const binding = normalizeBinding(rawBinding);
    if (!binding || bindingPolicyFailure(binding, platform)) continue;
    const parsed = parseBinding(binding);
    if (!parsed) continue;
    if (parsed.kind === "chord" && parsed.chord === leaderChord) continue;
    if (parsed.kind === "sequence" && parsed.leader !== leaderChord) continue;
    next[declaration.id] = binding;
  }

  const resolved = resolveKeymap(next, source);
  const invalid = new Set(leaderShadowedBindings(resolved));
  for (const conflict of detectConflicts(resolved)) {
    for (const id of conflict.bindingIds) {
      if (id in next) invalid.add(id);
    }
  }
  for (const conflict of detectSequenceConflicts(resolved)) {
    for (const id of conflict.bindingIds) {
      if (id in next) invalid.add(id);
    }
  }
  for (const id of invalid) delete next[id];
  return next;
}

export function syncShortcutPrefsFromSnapshot(): void {
  applyOverrides(resolveShortcutOverrides(getAppStateSnapshot().shortcuts));
}

export async function saveShortcutOverride(
  entryId: string,
  binding: string,
): Promise<ShortcutSaveResult> {
  const source = liveKeymapSource(shortcutPlatform());
  if (entryId === NAV_LEADER_ID) {
    const verdict = validateLeader(binding, source);
    if (!verdict.ok) return verdict;
    const previous = resolveKeymap(overrides(), source).leaderChord;
    const next = rebindLeaderOverrides(overrides(), verdict.chord, previous);
    const shadowed = leaderShadowedBindings(resolveKeymap(next, source));
    if (shadowed.length > 0) {
      return refuse(
        `${displayBinding(verdict.chord, shortcutPlatform())} is already used by ${labelForBinding(shadowed[0]!)}.`,
      );
    }
    applyOverrides(next);
    await persistAppStateInBackground({ shortcuts: { overrides: next } });
    return { ok: true };
  }

  const normalized = normalizeBinding(binding);
  if (!normalized) {
    return refuse("That key combination can’t be used as a shortcut.");
  }
  const next = { ...overrides(), [entryId]: normalized };
  const verdict = validateDeclarationBinding(entryId, normalized, next, source);
  if (!verdict.ok) return verdict;
  applyOverrides(next);
  await persistAppStateInBackground({ shortcuts: { overrides: next } });
  return { ok: true };
}

export async function clearShortcutOverride(entryId: string): Promise<void> {
  if (!(entryId in overrides())) return;
  let next = { ...overrides() };
  if (entryId === NAV_LEADER_ID) {
    const previous = resolveKeymap(
      next,
      liveKeymapSource(shortcutPlatform()),
    ).leaderChord;
    next = rebindLeaderOverrides(next, DEFAULT_NAV_LEADER_CHORD, previous);
  }
  delete next[entryId];
  applyOverrides(next);
  await persistAppStateInBackground({
    shortcuts:
      Object.keys(next).length > 0 ? { overrides: next } : undefined,
  });
}

export async function resetAllShortcuts(): Promise<void> {
  applyOverrides({});
  await persistAppStateInBackground({ shortcuts: undefined });
}

export function resetShortcutPrefsForTests(): void {
  applyOverrides({});
}
