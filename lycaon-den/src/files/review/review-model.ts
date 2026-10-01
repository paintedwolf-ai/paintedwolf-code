import type {
  SourceChangeOp,
  SourceHeadMatch,
  SourceSeenFile,
  SourceTip,
  SourceWalkEffect,
  SourceWalkFile,
} from "../../api/types.ts";
import type {
  DenReviewLensProjectState,
  DenReviewLensState,
} from "../../../shared/app-state-types.ts";
import type { AgentFileState } from "../components/agent-presence.ts";
import { effectObservedInCommand } from "../source/source-command-window.ts";
import { personLabel } from "../../components/source/annotations/source-contributor-label.ts";

/** Returns the effect that introduced the file to the range. */
export function oldestEffect(
  effects: readonly SourceWalkEffect[],
): SourceWalkEffect | null {
  return effects[effects.length - 1] ?? null;
}

export function tipSha(tip: SourceTip): string | null {
  return tip.state === "content" ? (tip.sha256?.trim() || null) : null;
}

export type FilesStagePaneMode = "files" | "review";

/**
 * The eye's comparison. `turn` and `session` read the chat selected in the
 * sidebar, so they follow it rather than naming one.
 */
export type ReviewLensScope =
  | { kind: "new" }
  | { kind: "turn" }
  | { kind: "session" }
  | { kind: "commit" }
  | { kind: "pin"; pinId: string; label?: string };

/** The chat a comparison is read for; `sessionScoped` means its own worktree answers. */
export type ScopeSubject = { sessionId: string; title: string; sessionScoped: boolean };

/** Only the turn and session comparisons are about the chat itself; the rest read a checkout. */
export function chatIsTheQuestion(scope: ReviewLensScope): boolean {
  return scope.kind === "turn" || scope.kind === "session";
}

/** The chat a request names: the one it asks about, or the one whose worktree answers it. */
export function subjectAddress(scope: ReviewLensScope, subject: ScopeSubject | null): string | undefined {
  if (!subject) return undefined;
  return chatIsTheQuestion(scope) || subject.sessionScoped ? subject.sessionId : undefined;
}

/** Spells the eye's choice for app state. */
export function encodeEyeChoice(scope: ReviewLensScope): string {
  return scope.kind === "pin" ? `pin:${scope.pinId.trim()}` : scope.kind;
}

export function parseEyeChoice(raw: string | null | undefined): ReviewLensScope | null {
  const s = (raw ?? "").trim();
  switch (s) {
    case "new":
    case "turn":
    case "session":
    case "commit":
      return { kind: s };
  }
  if (s.startsWith("pin:")) {
    const pinId = s.slice(4).trim();
    return pinId ? { kind: "pin", pinId } : null;
  }
  return null;
}

/**
 * The walk request's baseline. A chat comparison with no chat has none.
 * `turn:{session}` asks the host for the chat's current turn.
 */
export function requestBaseline(scope: ReviewLensScope, subject: ScopeSubject | null): string | null {
  switch (scope.kind) {
    case "new":
      return "presentation";
    case "turn":
      return subject ? `turn:${subject.sessionId}` : null;
    case "session":
      return subject ? `session:${subject.sessionId}` : null;
    case "commit":
      return "commit";
    case "pin":
      return `pin:${scope.pinId.trim()}`;
  }
}

/** One turn's baseline, as the host spells it. */
export function turnBaseline(sessionId: string, turn: number): string {
  return `turn:${sessionId.trim()},${turn}`;
}

/** The turn a resolved `turn:{session},{n}` baseline names. */
export function baselineTurn(baseline: string): number | null {
  const match = /^turn:[^,]+,(\d+)$/.exec(baseline.trim());
  return match ? Number(match[1]) : null;
}

export const REVIEW_LENS_PROJECT_CAP = 24;

/** Validates and bounds the persisted per-project eye choices. */
export function parseReviewLensState(
  value: unknown,
): DenReviewLensState | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const source = (value as { byProject?: unknown }).byProject;
  if (typeof source !== "object" || source === null || Array.isArray(source)) {
    return undefined;
  }

  const entries: Array<[string, DenReviewLensProjectState]> = [];
  for (const [projectId, value] of Object.entries(
    source as Record<string, unknown>,
  )) {
    const id = projectId.trim();
    if (!id || typeof value !== "object" || value === null || Array.isArray(value)) {
      continue;
    }
    const row = value as Record<string, unknown>;
    const scope = typeof row.comparison === "string" ? parseEyeChoice(row.comparison) : null;
    if (!scope) continue;
    const touchedAt =
      typeof row.touchedAt === "number" && Number.isFinite(row.touchedAt)
        ? row.touchedAt
        : 0;
    entries.push([
      id,
      {
        comparison: encodeEyeChoice(scope),
        ...(row.comparisonOff === true ? { comparisonOff: true } : {}),
        ...(row.markMyEdits === true ? { markMyEdits: true } : {}),
        ...(row.deletedLines === "inplace" ? { deletedLines: "inplace" as const } : {}),
        touchedAt,
      },
    ]);
  }
  if (entries.length === 0) return undefined;
  return {
    byProject: Object.fromEntries(
      entries
        .sort((a, b) => b[1].touchedAt - a[1].touchedAt)
        .slice(0, REVIEW_LENS_PROJECT_CAP),
    ),
  };
}

type LensCopyOpts = {
  /** The chat's turn is running — the list is still filling. */
  turnRunning?: boolean;
  /** Suppresses comparison copy while retaining the scope. */
  comparisonOff?: boolean;
  /** Title of the chat a chat comparison reads. */
  subjectTitle?: string;
};

export function lensScopeHeader(
  scope: ReviewLensScope,
  opts?: LensCopyOpts,
): string {
  if (opts?.comparisonOff) return "Nothing marked";
  switch (scope.kind) {
    case "new":
      return "New since you looked";
    case "turn":
      if (opts?.turnRunning) return "Changes in this turn, as they land";
      return "Changes in this turn";
    case "session":
      return opts?.subjectTitle?.trim()
        ? `Changes in “${opts.subjectTitle.trim()}”`
        : "Changes in this chat";
    case "commit":
      return "Changes since last commit";
    case "pin":
      return scope.label?.trim()
        ? `Changes since “${scope.label.trim()}”`
        : "Changes since this pin";
  }
}

export function lensEmptyCopy(
  scope: ReviewLensScope,
  opts?: LensCopyOpts,
): string {
  if (opts?.comparisonOff) {
    return "Marking is off. Choose a comparison above to see changes.";
  }
  switch (scope.kind) {
    case "new":
      return "Nothing new since you last looked.";
    case "turn":
      return "Nothing changed yet in this turn.";
    case "session":
      return "This chat hasn't changed any files.";
    case "commit":
      return "No changes since the last commit.";
    case "pin":
      return "No changes since this pin.";
  }
}

/** A chat comparison says so when no chat is selected, rather than claiming an empty list. */
export const LENS_NEEDS_CHAT_COPY = "Select a chat to see what it changed.";

/** Where a scope's range starts and ends, for a surface that reads it whole. */
export function lensRangeNote(scope: ReviewLensScope): string {
  const now = "compared with the files as they are now.";
  switch (scope.kind) {
    case "new":
      return `Everything that landed since you last looked at each file, ${now}`;
    case "turn":
      return `What has changed since the chat's current turn started, ${now}`;
    case "session":
      return `Everything this chat has changed, ${now}`;
    case "commit":
      return "Everything the working tree holds that HEAD does not. These rows come from Git, so a path with no recorded history carries no author and no write count.";
    case "pin":
      return `Everything that has changed since this pin, ${now}`;
  }
}

export function lensPickerLabel(
  scope: ReviewLensScope,
  opts?: LensCopyOpts,
): string {
  if (opts?.comparisonOff) return "Off";
  switch (scope.kind) {
    case "new":
      return "New since you looked";
    case "turn":
      return "This turn";
    case "session":
      return "Everything in this chat";
    case "commit":
      return "Since last commit";
    case "pin":
      return scope.label?.trim()
        ? scope.label.trim()
        : "Saved pin";
  }
}

export type ReviewContributor = {
  kind: "user" | "agent" | "command" | "external";
  sessionId: string;
  /** Person behind a user contribution; empty otherwise. */
  personId: string;
  label: string;
};

export function effectContributors(effect: SourceWalkEffect): ReviewContributor[] {
  return effect.contributors.map((author) => ({
    kind: author.origin === "external" && effectObservedInCommand(effect) ? "command" : author.origin,
    sessionId: author.session_id ?? "",
    personId: author.person_id ?? "",
    label: author.actor_label.trim(),
  }));
}

export function contributorLabel(contributor: ReviewContributor): string {
  switch (contributor.kind) {
    case "user": return personLabel(contributor.personId);
    case "agent": return contributor.label || "Agent";
    case "command": return "Command";
    case "external": return "Outside app";
  }
}

export type ReviewFileRow = {
  commit?: SourceWalkFile["commit"];
  fileId: string;
  rootId: string;
  path: string;
  basename: string;
  dirname: string;
  op: SourceChangeOp;
  lastTs: string | null;
  attribution: string;
  steps: SourceWalkEffect[];
  /** Content tip, or absent when the range deletes the file. */
  tip: SourceTip;
  /** Host comparison of the tip against HEAD; "unknown" whenever git cannot answer. */
  headMatch: SourceHeadMatch;
  unpresentedAgentEffects: number;
  presentationEffectId: string | null;
  presentationOrdinal: number | null;
  contributors: ReviewContributor[];
};

export function fileBasename(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] ?? path;
}

export function fileDirname(path: string): string {
  const slash = path.lastIndexOf("/");
  return slash < 0 ? "" : path.slice(0, slash);
}

export function countLabel(count: number, singular: string): string {
  return `${count} ${count === 1 ? singular : `${singular}s`}`;
}

export function opGlyph(op: SourceChangeOp): string {
  switch (op) {
    case "create":
      return "+";
    case "delete":
      return "−";
    case "rename":
      return "→";
    case "write":
    default:
      return "±";
  }
}

function dominantOp(effects: SourceWalkEffect[]): SourceChangeOp {
  if (effects.some((effect) => effect.op === "delete")) return "delete";
  // New to this range whenever the introducing effect was a create,
  // regardless of later writes.
  if (oldestEffect(effects)?.op === "create") return "create";
  if (effects.some((effect) => effect.op === "rename")) return "rename";
  return "write";
}

const CONTRIBUTOR_ORDER = ["user", "agent", "command", "external"] as const;

function contributorsOf(effects: SourceWalkEffect[]): ReviewContributor[] {
  const authors = new Map<string, ReviewContributor>();
  for (const effect of effects) {
    for (const author of effectContributors(effect)) {
      authors.set(`${author.kind}:${author.sessionId}:${author.personId}`, author);
    }
  }
  return [...authors.values()].sort((a, b) => CONTRIBUTOR_ORDER.indexOf(a.kind) - CONTRIBUTOR_ORDER.indexOf(b.kind)
    || contributorLabel(a).localeCompare(contributorLabel(b)) || a.sessionId.localeCompare(b.sessionId));
}

export function contributorsLabel(contributors: readonly ReviewContributor[]): string {
  return [...new Set(contributors.map(contributorLabel))].join(" + ") || "This project";
}

function toFileRow(f: SourceWalkFile): ReviewFileRow {
  const effects = f.effects;
  const contributors = contributorsOf(effects);
  return {
    commit: f.commit,
    fileId: f.file_id,
    rootId: f.root_id,
    path: f.path,
    basename: fileBasename(f.path),
    dirname: fileDirname(f.path),
    op: f.commit?.op ?? dominantOp(effects),
    lastTs: f.last_at?.trim() || null,
    attribution: f.commit
      ? contributors.length ? `Recorded history: ${contributorsLabel(contributors)}` : "No recorded history"
      : contributorsLabel(contributors),
    steps: effects,
    // Tip can include history outside the selected range.
    tip: f.tip,
    headMatch: f.head_match,
    unpresentedAgentEffects: f.unpresented_agent_effects,
    presentationEffectId: f.presentation_effect_id?.trim() || null,
    presentationOrdinal: f.presentation_ordinal ?? null,
    contributors,
  };
}

export function buildReviewFileRows(files: readonly SourceWalkFile[]): ReviewFileRow[] {
  return files.map(toFileRow);
}

/** A file whose latest look is still current. */
export type ReviewSeenRow = ReviewListRow & {
  seenAt: string;
  /** Names the look; marking the file unseen quotes it. */
  throughOrdinal: number;
};

/** Seen files keep the look's effects so the row reads like any other. */
export function buildSeenRows(files: readonly SourceSeenFile[]): ReviewSeenRow[] {
  return files.map((f) => {
    const contributors = contributorsOf(f.effects);
    return {
      fileId: f.file_id,
      rootId: f.root_id,
      path: f.path,
      basename: fileBasename(f.path),
      dirname: fileDirname(f.path),
      op: f.effects.length ? dominantOp(f.effects) : "write",
      lastTs: f.effects[0]?.observed_at?.trim() || null,
      attribution: contributorsLabel(contributors),
      steps: f.effects,
      tip: f.tip,
      headMatch: "unknown",
      unpresentedAgentEffects: 0,
      presentationEffectId: null,
      presentationOrdinal: null,
      contributors,
      liveVerb: null,
      sandbox: false,
      added: null,
      removed: null,
      seenAt: f.seen_at,
      throughOrdinal: f.through_ordinal,
    };
  });
}

export function unpresentedAgentFileCount(
  files: readonly SourceWalkFile[],
): number {
  let n = 0;
  for (const f of files) {
    if (f.unpresented_agent_effects > 0) n += 1;
  }
  return n;
}

export type ReviewListRow = ReviewFileRow & {
  /** In-flight verb, or null when idle. */
  liveVerb: string | null;
  /** Opens read-only when drafted in a worker overlay. */
  sandbox: boolean;
  /** Line add/remove counts when known. */
  added: number | null;
  removed: number | null;
};

/** Review lists what an agent is about to change; what it reads shows elsewhere. */
export type ReviewLiveState = Exclude<AgentFileState, "reading">;

/** A file an agent is changing now, as Review lists it. */
export type ReviewLiveRow = {
  rootId: string;
  path: string;
  state: ReviewLiveState;
  /** Root chat the work belongs to; workers fold into their chat. */
  chatId: string;
  chatTitle: string;
  /** The chat's user turn the work belongs to. */
  turn: number;
  /** Calls and worker jobs whose landing this row stands for. */
  toolCallIds: readonly string[];
  jobIds: readonly string[];
};

export type ReviewChangeStats = { added: number; removed: number };

export function reviewRowKey(rootId: string, path: string): string {
  return `${rootId}\u0000${path}`;
}

const LIVE_VERB: Record<ReviewLiveState, string> = {
  waiting: "waiting",
  editing: "editing",
  landing: "landing",
  ready: "ready to land",
  sandbox: "drafting",
  reserved: "reserved",
};

/** Worker drafts open read-only until they land. */
function liveSandbox(state: ReviewLiveState): boolean {
  return state === "sandbox" || state === "ready";
}

/** The comparison live rows are listed against. */
export type LiveRowScope = {
  scope: ReviewLensScope;
  subject: ScopeSubject | null;
  /** The turn a `turn` comparison resolved. */
  turn: number | null;
  comparisonOff: boolean;
};

/**
 * Live work the comparison will list once it lands. A chat comparison lists
 * only that chat's work, and a turn only that turn's, so nothing is shown that
 * the landed list would then drop.
 */
export function liveRowsInScope(
  rows: readonly ReviewLiveRow[],
  view: LiveRowScope,
): ReviewLiveRow[] {
  if (view.comparisonOff) return [...rows];
  switch (view.scope.kind) {
    case "turn":
      return rows.filter((row) =>
        row.chatId === view.subject?.sessionId && row.turn === view.turn);
    case "session":
      return rows.filter((row) => row.chatId === view.subject?.sessionId);
    default:
      return [...rows];
  }
}

function liveOnlyRow(live: ReviewLiveRow): ReviewListRow {
  return {
    fileId: "",
    rootId: live.rootId,
    path: live.path,
    basename: fileBasename(live.path),
    dirname: fileDirname(live.path),
    op: "write",
    lastTs: null,
    attribution: live.chatTitle,
    steps: [],
    tip: { state: "content" },
    headMatch: "unknown",
    unpresentedAgentEffects: 0,
    presentationEffectId: null,
    presentationOrdinal: null,
    contributors: [],
    liveVerb: LIVE_VERB[live.state],
    sandbox: liveSandbox(live.state),
    added: null,
    removed: null,
  };
}

/** Orders in-flight files before landed files; each file is one row. */
export function buildReviewRows(
  files: readonly ReviewFileRow[],
  live: readonly ReviewLiveRow[] = [],
  stats: ReadonlyMap<string, ReviewChangeStats> = new Map(),
): ReviewListRow[] {
  const liveByKey = new Map(live.map((row) => [reviewRowKey(row.rootId, row.path), row]));
  const landed: ReviewListRow[] = [];
  const liveLanded: ReviewListRow[] = [];
  const listed = new Set<string>();
  for (const f of files) {
    const key = reviewRowKey(f.rootId, f.path);
    listed.add(key);
    const current = liveByKey.get(key);
    const stat = f.commit ? undefined : stats.get(key);
    const row: ReviewListRow = {
      ...f,
      liveVerb: current ? LIVE_VERB[current.state] : null,
      sandbox: current ? liveSandbox(current.state) : false,
      added: stat?.added ?? null,
      removed: stat?.removed ?? null,
    };
    (current ? liveLanded : landed).push(row);
  }
  const liveOnly = live
    .filter((row) => !listed.has(reviewRowKey(row.rootId, row.path)))
    .map(liveOnlyRow);
  return [...liveOnly, ...liveLanded, ...landed];
}

/** Preview diffs carry their own content; they ignore the Review scope. */
export function previewDiffBufferJobId(path: string): string {
  return `diff:preview:${path}`;
}
