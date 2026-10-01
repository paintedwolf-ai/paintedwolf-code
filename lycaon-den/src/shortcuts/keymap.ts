import {
  chordHasNonShiftModifier,
  normalizeBinding,
  normalizeChord,
  parseBinding,
  serializeSequence,
} from "./chord.ts";

/** Command strata, ranked by the dispatcher. */
export type CommandScope = "global" | "files" | "composer" | "overlay";

/** The leader is a rebindable entry in its own right, not a declaration. */
export const NAV_LEADER_ID = "nav.leader";

export type KeymapDeclaration = {
  id: string;
  commandId: string;
  scope: CommandScope;
  /** Active host-resolved defaults for this platform. */
  defaults: readonly string[];
};

/** Contribution declarations plus the platform policy they resolve under. */
export type KeymapSource = {
  declarations: readonly KeymapDeclaration[];
  /** Full policy for user-authored values. */
  bindingAllowed: (binding: string) => boolean;
  /** Host-approved defaults still have to be syntactically producible. */
  defaultBindingAllowed: (binding: string) => boolean;
};

export const DEFAULT_NAV_LEADER_CHORD = "Mod+;";

const LEADER_PREFIX = "Leader ";

function leaderRelativeKey(binding: string): string | null {
  return binding.startsWith(LEADER_PREFIX)
    ? binding.slice(LEADER_PREFIX.length)
    : null;
}

export type ResolvedBinding = {
  bindingId: string;
  commandId: string;
  chord: string;
  scope: CommandScope;
};

export type ResolvedSequenceBinding = {
  bindingId: string;
  commandId: string;
  sequence: string;
  leader: string;
  secondKey: string;
  scope: CommandScope;
};

export type ResolvedKeymap = {
  /** Command ID → all live bindings across its declarations. */
  byCommand: Map<string, string[]>;
  /** Keybinding declaration ID → that declaration's live bindings. */
  byDeclaration: Map<string, string[]>;
  /** Chord → resolved bindings for fast O(1) shortcut dispatch. */
  byChord: Map<string, ResolvedBinding[]>;
  /** Leader + \0 + secondKey → resolved sequence bindings for fast O(1) sequence dispatch. */
  bySequence: Map<string, ResolvedSequenceBinding[]>;
  bindings: ResolvedBinding[];
  sequences: ResolvedSequenceBinding[];
  leaderChord: string;
};

export type KeymapConflict = {
  chord: string;
  scope: CommandScope;
  bindingIds: string[];
  commandIds: string[];
};

export type SequenceConflict = {
  scope: CommandScope;
  leader: string;
  secondKey: string;
  bindingIds: string[];
  commandIds: string[];
};

function resolveDeclaredBinding(
  raw: string,
  leaderChord: string,
  allowed: (binding: string) => boolean,
): string | null {
  const relative = leaderRelativeKey(raw);
  const binding = relative
    ? serializeSequence(leaderChord, relative)
    : normalizeBinding(raw);
  if (!binding || !allowed(binding)) return null;
  const parsed = parseBinding(binding);
  if (!parsed) return null;
  if (parsed.kind === "sequence" && parsed.leader !== leaderChord) return null;
  return binding;
}

function allCatalogSingleChords(source: KeymapSource): readonly string[] {
  const out = new Set<string>();
  for (const declaration of source.declarations) {
    for (const raw of declaration.defaults) {
      if (leaderRelativeKey(raw)) continue;
      const parsed = parseBinding(raw);
      if (parsed?.kind === "chord" && source.bindingAllowed(parsed.chord)) {
        out.add(parsed.chord);
      }
    }
  }
  return [...out];
}

/** True when a proposed leader equals an ordinary active catalog chord. */
export function leaderCollidesWithCatalog(
  leader: string,
  source: KeymapSource,
): boolean {
  const chord = normalizeChord(leader);
  if (!chord) return true;
  return allCatalogSingleChords(source).includes(chord);
}

function resolveLeader(
  overrides: Record<string, string> | null | undefined,
  source: KeymapSource,
): string {
  const raw = overrides?.[NAV_LEADER_ID];
  if (typeof raw !== "string") return DEFAULT_NAV_LEADER_CHORD;
  const chord = normalizeChord(raw);
  if (
    !chord ||
    !chordHasNonShiftModifier(chord) ||
    !source.bindingAllowed(chord) ||
    leaderCollidesWithCatalog(chord, source)
  ) {
    return DEFAULT_NAV_LEADER_CHORD;
  }
  return chord;
}

/** Resolve active defaults and declaration-specific user overrides. */
export function resolveKeymap(
  overrides: Record<string, string> | null | undefined,
  source: KeymapSource,
): ResolvedKeymap {
  const leaderChord = resolveLeader(overrides, source);
  const byCommand = new Map<string, string[]>();
  const byDeclaration = new Map<string, string[]>();
  const byChord = new Map<string, ResolvedBinding[]>();
  const bySequence = new Map<string, ResolvedSequenceBinding[]>();
  const bindings: ResolvedBinding[] = [];
  const sequences: ResolvedSequenceBinding[] = [];

  for (const declaration of source.declarations) {
    const resolveAll = (
      rawBindings: readonly string[],
      allowed: (binding: string) => boolean,
    ): string[] => {
      const values: string[] = [];
      for (const raw of rawBindings) {
        const binding = resolveDeclaredBinding(raw, leaderChord, allowed);
        if (binding && !values.includes(binding)) values.push(binding);
      }
      return values;
    };
    const override = overrides?.[declaration.id];
    const custom =
      typeof override === "string"
        ? resolveAll([override], source.bindingAllowed)
        : [];
    const resolved =
      typeof override === "string" && custom.length > 0
        ? custom
        : resolveAll(declaration.defaults, source.defaultBindingAllowed);

    for (const binding of resolved) {
      const parsed = parseBinding(binding);
      if (!parsed) continue;
      if (parsed.kind === "chord") {
        const row: ResolvedBinding = {
          bindingId: declaration.id,
          commandId: declaration.commandId,
          chord: parsed.chord,
          scope: declaration.scope,
        };
        bindings.push(row);
        const list = byChord.get(parsed.chord);
        if (list) list.push(row);
        else byChord.set(parsed.chord, [row]);
      } else {
        const row: ResolvedSequenceBinding = {
          bindingId: declaration.id,
          commandId: declaration.commandId,
          sequence: binding,
          leader: parsed.leader,
          secondKey: parsed.secondKey,
          scope: declaration.scope,
        };
        sequences.push(row);
        const seqKey = `${parsed.leader}\0${parsed.secondKey}`;
        const list = bySequence.get(seqKey);
        if (list) list.push(row);
        else bySequence.set(seqKey, [row]);
      }
    }
    byDeclaration.set(declaration.id, resolved);
    if (resolved.length === 0) continue;
    const commandBindings = byCommand.get(declaration.commandId) ?? [];
    for (const binding of resolved) {
      if (!commandBindings.includes(binding)) commandBindings.push(binding);
    }
    byCommand.set(declaration.commandId, commandBindings);
  }

  return {
    byCommand,
    byDeclaration,
    byChord,
    bySequence,
    bindings,
    sequences,
    leaderChord,
  };
}

/** Rebind the leader and preserve custom child sequences under it. */
export function rebindLeaderOverrides(
  overrides: Record<string, string>,
  nextLeader: string,
  previousLeader: string,
): Record<string, string> {
  const next: Record<string, string> = {
    ...overrides,
    [NAV_LEADER_ID]: nextLeader,
  };
  for (const [id, raw] of Object.entries(overrides)) {
    if (id === NAV_LEADER_ID) continue;
    const parsed = parseBinding(raw);
    if (!parsed || parsed.kind !== "sequence") continue;
    if (parsed.leader !== previousLeader) {
      delete next[id];
      continue;
    }
    const rewritten = serializeSequence(nextLeader, parsed.secondKey);
    if (rewritten) next[id] = rewritten;
    else delete next[id];
  }
  return next;
}

/** Same scope + same chord + at least two declarations is a conflict. */
export function detectConflicts(keymap: ResolvedKeymap): KeymapConflict[] {
  const groups = new Map<string, ResolvedBinding[]>();
  for (const binding of keymap.bindings) {
    const key = `${binding.scope}\0${binding.chord}`;
    groups.set(key, [...(groups.get(key) ?? []), binding]);
  }
  const conflicts: KeymapConflict[] = [];
  for (const [key, rows] of groups) {
    const bindingIds = [...new Set(rows.map((row) => row.bindingId))];
    if (bindingIds.length < 2) continue;
    const sep = key.indexOf("\0");
    conflicts.push({
      scope: key.slice(0, sep) as CommandScope,
      chord: key.slice(sep + 1),
      bindingIds,
      commandIds: [...new Set(rows.map((row) => row.commandId))],
    });
  }
  return conflicts;
}

/** Finds same-scope sequence conflicts. */
export function detectSequenceConflicts(
  keymap: ResolvedKeymap,
): SequenceConflict[] {
  const groups = new Map<string, ResolvedSequenceBinding[]>();
  for (const sequence of keymap.sequences) {
    const key = `${sequence.scope}\0${sequence.leader}\0${sequence.secondKey}`;
    groups.set(key, [...(groups.get(key) ?? []), sequence]);
  }
  const conflicts: SequenceConflict[] = [];
  for (const [key, rows] of groups) {
    const bindingIds = [...new Set(rows.map((row) => row.bindingId))];
    if (bindingIds.length < 2) continue;
    const scopeEnd = key.indexOf("\0");
    const leaderEnd = key.indexOf("\0", scopeEnd + 1);
    conflicts.push({
      scope: key.slice(0, scopeEnd) as CommandScope,
      leader: key.slice(scopeEnd + 1, leaderEnd),
      secondKey: key.slice(leaderEnd + 1),
      bindingIds,
      commandIds: [...new Set(rows.map((row) => row.commandId))],
    });
  }
  return conflicts;
}

/** Declaration IDs shadowed by the resolved leader prefix. */
export function leaderShadowedBindings(keymap: ResolvedKeymap): string[] {
  return [
    ...new Set(
      keymap.bindings
        .filter((binding) => binding.chord === keymap.leaderChord)
        .map((binding) => binding.bindingId),
    ),
  ];
}

export function commandsForChord(
  keymap: ResolvedKeymap,
  chord: string,
): ResolvedBinding[] {
  return keymap.byChord?.get(chord) ?? [];
}

export function commandsForSequence(
  keymap: ResolvedKeymap,
  leader: string,
  secondKey: string,
): ResolvedSequenceBinding[] {
  return keymap.bySequence?.get(`${leader}\0${secondKey}`) ?? [];
}

export function leaderIsPrefix(keymap: ResolvedKeymap, chord: string): boolean {
  return keymap.leaderChord === chord;
}
