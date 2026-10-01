import type { AttentionRow, ContributionCommand, SearchHit, SourceRevisionComparison } from "../api/types.ts";
import type { InventoryFile } from "../files/tree/file-inventory.ts";
import { filterSymbolsByQuery } from "../files/tree/file-inventory.ts";
import { attentionRank, compareAttention, isBlocking } from "../attention/attention-model.ts";
import {
  COMMAND_GROUP_ORDER,
  commandGroupLabel,
} from "../shortcuts/command-display.ts";
import { matchActions, matchesAllTerms, splitQueryTerms } from "../shortcuts/match-actions.ts";
import {
  DSL_FIELD_ALLOWLIST,
  filterToken,
  queryFilterOccurrences,
} from "./search-query-model.ts";
import {
  SEARCH_RESULT_TYPES,
  SEARCH_RESULT_TYPE_BY_ID,
  hitInResultType,
  isSearchResultTypeId,
  type SearchResultTypeId,
} from "./search-result-types.ts";
import {
  CROSSBAR_MODES,
  type CrossbarMode,
} from "./crossbar-modes.ts";

/** Maximum actions in Everything. */
export const CROSSBAR_ACTION_CAP = 5;
/** Maximum federated hits in a single-group mode. */
export const CROSSBAR_HIT_CAP = 5;
/** Maximum federated hits per group section in Everything. */
export const CROSSBAR_GROUP_HIT_CAP = 4;
/** Section order follows navigation intent: file, declaration, code, conversation. */
export const CROSSBAR_EVERYTHING_GROUP_ORDER: readonly SearchResultTypeId[] = [
  "files",
  "symbols",
  "code",
  "messages",
  "evidence",
];
/** Maximum rows the `@` arm lists from the active file. */
export const CROSSBAR_ARM_CAP = 50;
/** Symbol names shorter than this match nothing, as on the host. */
const CROSSBAR_SYMBOL_MIN_CHARS = 2;
/** Maximum idle recent queries. */
export const CROSSBAR_RECENT_CAP = 4;
/** Maximum matching navigation targets. */
export const CROSSBAR_GOTO_CAP = 5;
/** Maximum idle chat targets. */
export const CROSSBAR_IDLE_CHAT_CAP = 3;
/** Maximum blocked chat targets. */
export const CROSSBAR_NEEDS_YOU_CAP = 5;

/** Closed navigation surface ids. */
export type CrossbarSurfaceId =
  | "context"
  | "files"
  | "search"
  | "security"
  | "cost"
  | "artifacts"
  | "blueprints"
  | "extensions"
  | "chats"
  | "home"
  | "settings";

export type CrossbarGotoTarget =
  | {
      kind: "session";
      projectId: string;
      sessionId: string;
      label: string;
      context?: string;
      /** Current cross-project attention state. */
      attention?: AttentionRow;
    }
  | { kind: "project"; projectId: string; label: string; context?: string }
  | {
      kind: "surface";
      surface: CrossbarSurfaceId;
      label: string;
      context?: string;
    }
  | {
      /** One individual setting; the host shell reveals its row. */
      kind: "setting";
      settingId: string;
      label: string;
      context?: string;
      /** Matched but never shown. */
      keywords?: readonly string[];
    };

export const CROSSBAR_GOTO_KIND_LABEL: Record<
  CrossbarGotoTarget["kind"],
  string
> = {
  session: "Chat",
  project: "Project",
  surface: "View",
  setting: "Setting",
};

/** One declaration in the active file, for the `@` arm. */
export type CrossbarSymbolRow = {
  name: string;
  kind: string;
  line: number;
  rootId: string;
  path: string;
  bufferKey: string;
  /** UTF-16 indexes into name that the query matched. */
  matchIndexes?: readonly number[];
};

/** The file a line jump lands in. */
export type CrossbarLineTarget = {
  rootId: string;
  path: string;
  jobId?: string;
};

/** An absolute path the host found outside every project root. */
export type CrossbarOutsidePath = {
  path: string;
  kind: "file" | "directory";
};

/** What a query's leading character asks Crossbar for. */
export type CrossbarArm =
  /** `#` searches the Symbols tab without leaving the query. */
  | { kind: "search"; text: string; mode?: "symbols" }
  | { kind: "actions"; text: string }
  | { kind: "file-symbols"; text: string }
  /** `:42` or `:42:5`; line is 0 until a number is typed. */
  | { kind: "line"; line: number; column?: number };

const LINE_ARM = /^:(\d*)(?::(\d*))?$/;

export function crossbarArm(query: string): CrossbarArm {
  const trimmed = query.trim();
  const line = LINE_ARM.exec(trimmed);
  if (line) {
    const column = Number(line[2]);
    return { kind: "line", line: Number(line[1]) || 0, ...(column >= 1 ? { column } : {}) };
  }
  const text = trimmed.slice(1).trim();
  if (trimmed.startsWith(">")) return { kind: "actions", text };
  if (trimmed.startsWith("@")) return { kind: "file-symbols", text };
  if (trimmed.startsWith("#")) return { kind: "search", text, mode: "symbols" };
  return { kind: "search", text: trimmed };
}

/** `>` switches to Actions and `#` to Symbols without leaving the query. */
export function effectiveCrossbarMode(query: string, mode: CrossbarMode): CrossbarMode {
  const arm = crossbarArm(query);
  if (arm.kind === "actions") return "actions";
  if (arm.kind === "search" && arm.mode) return arm.mode;
  return mode;
}

export type CrossbarRow =
  | {
      kind: "section";
      id: string;
      label: string;
      /** Family whose full result set the header offers to open. */
      more?: SearchResultTypeId;
    }
  | { kind: "action"; id: string; command: ContributionCommand }
  | { kind: "hit"; id: string; hit: SearchHit }
  | { kind: "inventory-file"; id: string; file: InventoryFile }
  | { kind: "symbol"; id: string; symbol: CrossbarSymbolRow }
  | { kind: "line"; id: "line"; line: number; column?: number; target: CrossbarLineTarget }
  | { kind: "revision"; id: string; comparison: SourceRevisionComparison }
  | {
      kind: "outside";
      id: string;
      action: "open-project" | "reveal";
      outside: CrossbarOutsidePath;
    }
  | { kind: "recent"; id: string; query: string }
  | { kind: "goto"; id: string; target: CrossbarGotoTarget }
  | { kind: "escalate"; id: "escalate" };

export function gotoTargetId(target: CrossbarGotoTarget): string {
  if (target.kind === "session") {
    return `goto:session:${target.projectId}:${target.sessionId}`;
  }
  if (target.kind === "project") return `goto:project:${target.projectId}`;
  if (target.kind === "setting") return `goto:setting:${target.settingId}`;
  return `goto:surface:${target.surface}`;
}

/** Matches labels and context — every query term, any haystack. */
export function matchGotoTargets(
  targets: readonly CrossbarGotoTarget[],
  query: string,
): CrossbarGotoTarget[] {
  const terms = splitQueryTerms(query);
  if (!terms.length) return [];
  return targets.filter((t) =>
    matchesAllTerms(
      [
        t.label.toLowerCase(),
        (t.context ?? "").toLowerCase(),
        ...(t.kind === "setting" ? (t.keywords ?? []).map((k) => k.toLowerCase()) : []),
      ],
      terms,
    ),
  );
}

type SessionTarget = Extract<CrossbarGotoTarget, { kind: "session" }>;

export function isSessionTarget(
  target: CrossbarGotoTarget,
): target is SessionTarget {
  return target.kind === "session";
}

/** Returns blocked chats by urgency and age. */
export function blockedSessionTargets(
  targets: readonly CrossbarGotoTarget[],
): SessionTarget[] {
  return targets
    .filter(isSessionTarget)
    .filter((t) => isBlocking(t.attention?.class))
    .sort((a, b) => compareAttention(a.attention, b.attention));
}

/** Orders idle chats by attention and origin project. */
export function orderIdleChatTargets(
  targets: readonly CrossbarGotoTarget[],
  originProjectId: string | null,
): SessionTarget[] {
  return targets.filter(isSessionTarget).sort((a, b) => {
    const byAttention = compareAttention(a.attention, b.attention);
    if (byAttention !== 0) return byAttention;
    if (originProjectId) {
      const aHome = a.projectId === originProjectId ? 0 : 1;
      const bHome = b.projectId === originProjectId ? 0 : 1;
      if (aHome !== bHome) return aHome - bHome;
    }
    return 0;
  });
}

/** Orders navigation targets by attention and origin project. */
export function orderGotoTargets(
  targets: readonly CrossbarGotoTarget[],
  originProjectId: string | null,
): CrossbarGotoTarget[] {
  return [...targets].sort((a, b) => {
    const aAttention = isSessionTarget(a) ? a.attention : undefined;
    const bAttention = isSessionTarget(b) ? b.attention : undefined;
    const rank = attentionRank(aAttention?.class) - attentionRank(bAttention?.class);
    if (rank !== 0) return rank;
    if (aAttention && bAttention) {
      const byAge = compareAttention(aAttention, bAttention);
      if (byAge !== 0) return byAge;
    }
    if (originProjectId && isSessionTarget(a) && isSessionTarget(b)) {
      const aHome = a.projectId === originProjectId ? 0 : 1;
      const bHome = b.projectId === originProjectId ? 0 : 1;
      if (aHome !== bHome) return aHome - bHome;
    }
    return 0;
  });
}

export function cycleCrossbarMode(
  mode: CrossbarMode,
  dir: 1 | -1,
): CrossbarMode {
  const idx = CROSSBAR_MODES.indexOf(mode);
  const next = (idx + dir + CROSSBAR_MODES.length) % CROSSBAR_MODES.length;
  return CROSSBAR_MODES[next] ?? "everything";
}

/** Matches only declared query fields. */
const DSL_FIELD_TOKEN = new RegExp(
  `(?:^|\\s)(?:${DSL_FIELD_ALLOWLIST.join("|")}):`,
  "i",
);

export function queryLooksLikeDsl(query: string): boolean {
  return DSL_FIELD_TOKEN.test(query.trim());
}

export function isFileHit(hit: SearchHit): boolean {
  return hit.hit_kind.trim().toLowerCase() === "file";
}

export function filterHitsForMode(
  hits: readonly SearchHit[],
  mode: CrossbarMode,
): SearchHit[] {
  if (mode === "actions") return [];
  if (isSearchResultTypeId(mode)) return hits.filter((h) => hitInResultType(h, mode));
  return [...hits];
}

export type CrossbarHitGroup = {
  id: SearchResultTypeId | "other";
  label: string;
  hits: SearchHit[];
};

/** Groups hits into Everything sections; ungrouped kinds trail as Other. */
export function groupHitsForEverything(
  hits: readonly SearchHit[],
): CrossbarHitGroup[] {
  const groups: CrossbarHitGroup[] = CROSSBAR_EVERYTHING_GROUP_ORDER.map(
    (id) => ({
      id,
      label: SEARCH_RESULT_TYPE_BY_ID[id].label,
      hits: hits.filter((h) => hitInResultType(h, id)),
    }),
  );
  const other = hits.filter(
    (h) => !SEARCH_RESULT_TYPES.some((type) => hitInResultType(h, type.id)),
  );
  if (other.length > 0) groups.push({ id: "other", label: "Other", hits: other });
  return groups.filter((group) => group.hits.length > 0);
}

export function actionableCrossbarRows(
  rows: readonly CrossbarRow[],
): Array<Exclude<CrossbarRow, { kind: "section" }>> {
  return rows.filter(
    (r): r is Exclude<CrossbarRow, { kind: "section" }> => r.kind !== "section",
  );
}

export function nextCrossbarIndex(
  current: number,
  length: number,
  delta: 1 | -1,
): number {
  if (length <= 0) return 0;
  if (current < 0) return delta === 1 ? 0 : length - 1;
  return (current + delta + length) % length;
}

export type BuildCrossbarRowsArgs = {
  query: string;
  mode: CrossbarMode;
  hits: readonly SearchHit[];
  /** Whether to show full-search escalation. */
  includeEscalate: boolean;
  /** Recent queries, newest first. */
  recentQueries?: readonly string[];
  /** Recent action ids. */
  recentActionIds?: readonly string[];
  /** Navigation targets in source order. */
  gotoTargets?: readonly CrossbarGotoTarget[];
  /** Origin project used for tie-breaking. */
  originProjectId?: string | null;
  /** Local file inventory rows. */
  inventoryFiles?: readonly InventoryFile[];
  /** The active file's declarations, for the `@` arm. */
  symbolRows?: readonly CrossbarSymbolRow[];
  /** The file a `:line` query jumps within. */
  lineTarget?: CrossbarLineTarget | null;
  /** An absolute query outside every root. */
  outside?: CrossbarOutsidePath | null;
  /** Whether this device can reveal outside paths. */
  canRevealOutside?: boolean;
  /** Git comparisons a revision-shaped query resolved to. */
  revisions?: readonly SourceRevisionComparison[];
  /** Relevant commands from the hydrated frame. */
  contributionCommands?: readonly ContributionCommand[];
};

export function shouldRunCrossbarServerSearch(
  query: string,
  mode: CrossbarMode,
  hasLocalFileArm: boolean,
): boolean {
  const arm = crossbarArm(query);
  if (arm.kind !== "search" || !arm.text || crossbarAwaitsSymbolName(query, mode)) return false;
  const effective = effectiveCrossbarMode(query, mode);
  if (effective === "actions") return false;
  if (effective === "files" && hasLocalFileArm && !queryLooksLikeDsl(arm.text)) {
    return false;
  }
  return true;
}

/** The host query for a Crossbar query: its text, narrowed to the tab's
 * family unless the text already chooses a kind. */
export function crossbarServerQuery(
  query: string,
  mode: CrossbarMode,
): string {
  const arm = crossbarArm(query);
  const trimmed = arm.kind === "line" ? "" : arm.text;
  const effective = effectiveCrossbarMode(query, mode);
  if (!trimmed || !isSearchResultTypeId(effective)) return trimmed;
  const occurrences = queryFilterOccurrences(trimmed);
  if (
    occurrences.some(
      (occurrence) => !occurrence.negated && occurrence.field === "kind",
    )
  ) {
    return trimmed;
  }
  const missingKinds = SEARCH_RESULT_TYPE_BY_ID[effective].kinds.filter(
    (kind) =>
      !occurrences.some(
        (occurrence) =>
          occurrence.negated &&
          occurrence.field === "kind" &&
          occurrence.value.toLowerCase() === kind.toLowerCase(),
      ),
  );
  if (missingKinds.length === 0) return trimmed;
  const clauses = missingKinds.map((kind) => filterToken("kind", kind));
  const prefix =
    clauses.length === 1 ? clauses[0]! : `(${clauses.join(" OR ")})`;
  return `${prefix} ${trimmed}`.trim();
}

export function buildCrossbarRows(args: BuildCrossbarRowsArgs): CrossbarRow[] {
  const arm = crossbarArm(args.query);
  const mode = effectiveCrossbarMode(args.query, args.mode);
  // Arms match the query without their prefix.
  const trimmed = arm.kind === "line" ? "" : arm.text;
  const rows: CrossbarRow[] = [];

  if (arm.kind === "line") {
    if (args.lineTarget && arm.line > 0) {
      rows.push({
        kind: "line",
        id: "line",
        line: arm.line,
        ...(arm.column != null ? { column: arm.column } : {}),
        target: args.lineTarget,
      });
    }
    return rows;
  }
  if (arm.kind === "file-symbols") {
    const symbols = arm.text ? filterSymbolsByQuery(args.symbolRows ?? [], arm.text) : [...(args.symbolRows ?? [])];
    // The status row covers an empty symbol result.
    if (symbols.length > 0) {
      rows.push({ kind: "section", id: "section-symbols", label: "Symbols" });
      pushSymbolRows(rows, symbols.slice(0, CROSSBAR_ARM_CAP));
    }
    return rows;
  }

  const wantActions = mode === "everything" || mode === "actions";
  const wantHits = arm.kind === "search" && (mode === "everything" || isSearchResultTypeId(mode));
  const wantInventory =
    arm.kind === "search" &&
    args.inventoryFiles &&
    args.inventoryFiles.length > 0 &&
    (mode === "everything" || mode === "files") &&
    !queryLooksLikeDsl(trimmed);
  const wantOutside = arm.kind === "search" && args.outside != null && (mode === "everything" || mode === "files");

  // Navigation targets stay in Everything; blocked chats lead the idle list.
  let needsYou: CrossbarGotoTarget[] = [];
  let needsYouLabel = "Needs you";
  let gotos: CrossbarGotoTarget[] = [];
  let gotoLabel = "Go to";
  const origin = args.originProjectId ?? null;
  if (mode === "everything" && args.gotoTargets?.length) {
    if (!trimmed) {
      const blocked = blockedSessionTargets(args.gotoTargets);
      needsYou = blocked.slice(0, CROSSBAR_NEEDS_YOU_CAP);
      // Keep the full blocked count visible when rows are capped.
      needsYouLabel =
        blocked.length > needsYou.length
          ? `Needs you (${blocked.length})`
          : "Needs you";
      // Exclude every blocked chat from the regular Chats section.
      gotos = orderIdleChatTargets(args.gotoTargets, origin)
        .filter((t) => !isBlocking(t.attention?.class))
        .slice(0, CROSSBAR_IDLE_CHAT_CAP);
      gotoLabel = "Chats";
    } else if (!queryLooksLikeDsl(trimmed)) {
      gotos = orderGotoTargets(
        matchGotoTargets(args.gotoTargets, trimmed),
        origin,
      ).slice(0, CROSSBAR_GOTO_CAP);
    }
  }

  const recents =
    !trimmed && mode !== "actions"
      ? (args.recentQueries ?? [])
          .map((q) => q.trim())
          .filter(Boolean)
          .slice(0, CROSSBAR_RECENT_CAP)
      : [];

  let actions: ContributionCommand[] = [];
  if (wantActions && (!trimmed || !queryLooksLikeDsl(trimmed) || mode === "actions")) {
    actions = matchActions(trimmed, {
      candidates: args.contributionCommands ?? [],
      recentIds: args.recentActionIds,
    });
    if (mode === "everything") actions = actions.slice(0, CROSSBAR_ACTION_CAP);
  }

  // The local file arm already lists files; federated file hits then stay out.
  const hits = wantHits
    ? filterHitsForMode(args.hits, mode).filter(
        (h) => !(wantInventory && isFileHit(h)),
      )
    : [];

  if (wantInventory) {
    rows.push({ kind: "section", id: "section-files", label: "Files" });
    for (const file of args.inventoryFiles!.slice(0, CROSSBAR_HIT_CAP)) {
      rows.push({
        kind: "inventory-file",
        id: `inv:${file.rootId}:${file.path}`,
        file,
      });
    }
  }

  if (wantOutside) {
    const outside = args.outside!;
    rows.push({ kind: "section", id: "section-outside", label: "Outside this project" });
    rows.push({ kind: "outside", id: "outside:open-project", action: "open-project", outside });
    if (args.canRevealOutside) {
      rows.push({ kind: "outside", id: "outside:reveal", action: "reveal", outside });
    }
  }

  if (arm.kind === "search" && mode === "everything" && args.revisions?.length) {
    rows.push({ kind: "section", id: "section-revisions", label: "Changes" });
    for (const comparison of args.revisions.slice(0, CROSSBAR_GROUP_HIT_CAP)) {
      rows.push({
        kind: "revision",
        id: `revision:${comparison.root_id}:${comparison.before_commit}:${comparison.after_commit}`,
        comparison,
      });
    }
  }

  if (needsYou.length > 0) {
    rows.push({ kind: "section", id: "section-needs-you", label: needsYouLabel });
    for (const target of needsYou) {
      rows.push({ kind: "goto", id: gotoTargetId(target), target });
    }
  }

  if (gotos.length > 0) {
    rows.push({ kind: "section", id: "section-goto", label: gotoLabel });
    for (const target of gotos) {
      rows.push({ kind: "goto", id: gotoTargetId(target), target });
    }
  }

  if (recents.length > 0) {
    rows.push({ kind: "section", id: "section-recent", label: "Recent" });
    for (const query of recents) {
      rows.push({ kind: "recent", id: `recent:${query}`, query });
    }
  }

  if (actions.length > 0) {
    if (mode === "everything") {
      rows.push({ kind: "section", id: "section-actions", label: "Actions" });
      for (const command of actions) {
        rows.push({ kind: "action", id: `action:${command.id}`, command });
      }
    } else {
      // Reuse keyboard-settings group labels.
      const byGroup = new Map<string, ContributionCommand[]>();
      for (const command of actions) {
        const key = command.category ?? "other";
        byGroup.set(key, [...(byGroup.get(key) ?? []), command]);
      }
      const authoredGroups = [...byGroup.keys()]
        .filter((group) => group !== "other" && !COMMAND_GROUP_ORDER.includes(group))
        .sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
      for (const group of [...COMMAND_GROUP_ORDER, ...authoredGroups, "other"]) {
        const members = byGroup.get(group);
        if (!members?.length) continue;
        rows.push({
          kind: "section",
          id: group === "other" ? "section-actions" : `section-actions-${rows.length}`,
          label: group === "other" ? "Actions" : commandGroupLabel(group),
        });
        for (const command of members) {
          rows.push({ kind: "action", id: `action:${command.id}`, command });
        }
      }
    }
  }

  // hit_id is the wire's stable identity; a composed key can collide
  // (same file, same source_ref) and misdirect hover and Enter.
  if (mode === "everything") {
    for (const group of groupHitsForEverything(hits)) {
      const shown = group.hits.slice(0, CROSSBAR_GROUP_HIT_CAP);
      rows.push({
        kind: "section",
        id: `section-hits-${group.id}`,
        label: group.label,
        ...(group.id !== "other" && shown.length < group.hits.length
          ? { more: group.id }
          : {}),
      });
      for (const hit of shown) {
        rows.push({ kind: "hit", id: `hit:${hit.hit_id}`, hit });
      }
    }
  } else {
    for (const hit of hits.slice(0, CROSSBAR_HIT_CAP)) {
      rows.push({ kind: "hit", id: `hit:${hit.hit_id}`, hit });
    }
  }

  if (args.includeEscalate && (trimmed.length > 0 || hits.length > 0)) {
    rows.push({ kind: "escalate", id: "escalate" });
  }

  return rows;
}

function pushSymbolRows(rows: CrossbarRow[], symbols: readonly CrossbarSymbolRow[]) {
  for (const symbol of symbols) {
    rows.push({
      kind: "symbol",
      id: `symbol:${symbol.rootId}:${symbol.path}:${symbol.line}:${symbol.name}`,
      symbol,
    });
  }
}

export type CrossbarHitStatus = "idle" | "searching" | "empty" | "hits";

/** Returns the settled federated search status. */
export function crossbarHitStatus(args: {
  query: string;
  mode: CrossbarMode;
  searching: boolean;
  hitCount: number;
}): CrossbarHitStatus {
  if (args.mode === "actions" || !args.query.trim()) return "idle";
  if (args.hitCount > 0) return "hits";
  return args.searching ? "searching" : "empty";
}

/** Returns settled empty-state copy. */
export function crossbarNoMatchLabel(mode: CrossbarMode): string {
  if (isSearchResultTypeId(mode)) return SEARCH_RESULT_TYPE_BY_ID[mode].emptyLabel;
  return "No matches";
}

/** The Symbols tab is waiting for enough of a name to match. Length is in
 * code points, as the host counts it. */
export function crossbarAwaitsSymbolName(query: string, mode: CrossbarMode): boolean {
  const arm = crossbarArm(query);
  if (arm.kind !== "search" || effectiveCrossbarMode(query, mode) !== "symbols") return false;
  let codePoints = 0;
  for (const _ of arm.text) {
    if (++codePoints >= CROSSBAR_SYMBOL_MIN_CHARS) return false;
  }
  return true;
}

export function canEscalateCrossbar(
  query: string,
  hitCount: number,
): boolean {
  return query.trim().length > 0 || hitCount > 0;
}
