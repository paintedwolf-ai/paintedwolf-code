import type {
  SourceChangeOp,
  SourceSeenFile,
  SourceSeenList,
  SourceTip,
  SourceWalkEffect,
  SourceWalkFile,
} from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceRangeTarget } from "../../api/http-capabilities/source-history.ts";
import {
  baselineTurn,
  encodeEyeChoice,
  oldestEffect,
  requestBaseline,
  type ReviewLensScope,
  subjectAddress,
  type ScopeSubject,
} from "../review/review-model.ts";
import {
  getMarkMyEdits,
  getSeenLimit,
  getSidebarScope,
  isComparisonOff,
  subscribeReviewScope,
} from "../review/review-pane.ts";
import {
  scopeChangeKindFromFlags,
  type ScopeChangeKind,
} from "../components/files-scope-change-mark.ts";
import { reportSurfaceFailure, type SurfaceFailureCopy } from "../../notices/surface-failure.ts";

const SCOPE_UNAVAILABLE: SurfaceFailureCopy = {
  code: "files_review_unavailable",
  title: "Review unavailable",
  suggestedAction: "Reopen the review or pick the comparison again.",
};
import { mergeSourceWalkFiles } from "../source/source-walk-pages.ts";

type ScopeEffect = {
  fileId: string;
  tip: SourceTip;
  entryOp: SourceChangeOp;
  /** The effect that brought the file into the range; empty for a commit row. */
  entryEffectId: string;
  commit?: SourceWalkFile["commit"];
};

type ResolvedScope = {
  scope: ReviewLensScope;
  /** The chat the answer was read for; null when none was selected. */
  subject: ScopeSubject | null;
  /** Identifies what was asked; a refresh of the same request keeps its answer on screen. */
  request: string;
  /** The host's baseline for this answer, which per-file comparisons address. Empty while off or without a chat. */
  baseline: string;
  /** The user turn a `turn` comparison read. */
  turn: number | null;
  status: "empty" | "resolving" | "ready" | "error";
  error: string | null;
  /** Settled content stays visible during refresh. */
  settled: boolean;
  /** A chat comparison with no chat selected lists nothing and claims nothing. */
  needsChat: boolean;
  /** Marking was off; the scope is kept so turning it on lands where it left. */
  comparisonOff: boolean;
  /** Commit comparisons mark all changes; ledger comparisons can omit the person's edits. */
  markUserEdits: boolean;
  commitAvailable: boolean;
  files: readonly SourceWalkFile[];
  paths: ReadonlySet<string>;
  effects: ReadonlyMap<string, ScopeEffect>;
  deleted: ReadonlySet<string>;
  added: ReadonlySet<string>;
  /** Files whose latest look is still current, newest look first; `new` scope only. */
  seen: readonly SourceSeenFile[];
  /** Older looks exist beyond the listed page. */
  seenMore: boolean;
};

const FILE_LIMIT = 500;

const NO_SEEN: Pick<ResolvedScope, "seen" | "seenMore"> = {
  seen: [],
  seenMore: false,
};

const records = new Map<string, ResolvedScope>();
/** The newest resolve claimed per project; a flight another claim superseded drops its answer. */
const epochs = new Map<string, number>();
/** The claim behind the newest landed answer; tells readers a published host fact reached the record. */
const settledEpochs = new Map<string, number>();
const settledListeners = new Set<(projectId: string) => void>();
const resolvers = new Map<string, () => void>();
const listeners = new Set<(projectId: string) => void>();
/** Bumped when the decorations the tree draws differ. */
const markEpochs = new Map<string, number>();
/** Bumped when the resolved ledger differs, ignoring in-flight status. */
const contentEpochs = new Map<string, number>();
/** Last record with a terminal status, reused when a resolve is a no-op. */
const settledRecords = new Map<string, ResolvedScope>();

function fileKey(rootId: string, path: string): string {
  return `${rootId}\0${path}`;
}

function emptyRecord(): ResolvedScope {
  return {
    scope: { kind: "new" },
    subject: null,
    request: "",
    baseline: "",
    turn: null,
    status: "empty",
    error: null,
    settled: false,
    needsChat: false,
    comparisonOff: false,
    markUserEdits: true,
    commitAvailable: false,
    files: [],
    paths: new Set(),
    effects: new Map(),
    deleted: new Set(),
    added: new Set(),
    ...NO_SEEN,
  };
}

/** A file listed as new is not also seen, whichever request answered first. */
function seenOf(
  list: SourceSeenList,
  newFiles: readonly SourceWalkFile[],
): Pick<ResolvedScope, "seen" | "seenMore"> {
  const fresh = new Set(newFiles.map((f) => f.file_id));
  const seen = list.files.filter((f) => !fresh.has(f.file_id));
  return {
    seen,
    seenMore: Boolean(list.next_cursor),
  };
}

function sameSeen(a: readonly SourceSeenFile[], b: readonly SourceSeenFile[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    const x = a[i]!;
    const y = b[i]!;
    if (x.file_id !== y.file_id || x.root_id !== y.root_id || x.path !== y.path) return false;
    if (x.through_ordinal !== y.through_ordinal || x.seen_at !== y.seen_at) return false;
    if (!sameTip(x.tip, y.tip) || !sameEffectRows(x.effects, y.effects)) return false;
  }
  return true;
}

function indexFiles(files: readonly SourceWalkFile[]): {
  paths: Set<string>;
  effects: Map<string, ScopeEffect>;
  deleted: Set<string>;
  added: Set<string>;
} {
  const paths = new Set<string>();
  const effects = new Map<string, ScopeEffect>();
  const deleted = new Set<string>();
  const added = new Set<string>();
  for (const f of files) {
    const entry = oldestEffect(f.effects);
    const tip = f.tip;
    // A mark promises a baseline comparison for this file-specific range.
    if (!f.commit && !entry?.id.trim()) continue;
    if (!f.commit && tip.state === "content" && !tip.sha256?.trim()) continue;
    const key = fileKey(f.root_id, f.path);
    const entryOp = f.commit?.op ?? entry?.op ?? "write";
    paths.add(key);
    effects.set(key, {
      fileId: f.file_id,
      tip,
      entryOp,
      entryEffectId: entry?.id ?? "",
      commit: f.commit,
    });
    if (tip.state === "absent") deleted.add(key);
    // A deleted outcome takes precedence over an added entry.
    else if (entryOp === "create") added.add(key);
  }
  return { paths, effects, deleted, added };
}

function sameKeySet(a: ReadonlySet<string>, b: ReadonlySet<string>): boolean {
  if (a === b) return true;
  if (a.size !== b.size) return false;
  for (const key of a) if (!b.has(key)) return false;
  return true;
}

function sameTip(a: SourceTip, b: SourceTip): boolean {
  return a.state === b.state && (a.sha256 ?? "") === (b.sha256 ?? "");
}

function sameEffectRows(
  a: readonly SourceWalkEffect[],
  b: readonly SourceWalkEffect[],
): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    const x = a[i]!;
    const y = b[i]!;
    if (x.id !== y.id || x.ordinal !== y.ordinal || x.origin !== y.origin) {
      return false;
    }
  }
  return true;
}

function sameFiles(
  a: readonly SourceWalkFile[],
  b: readonly SourceWalkFile[],
): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    const x = a[i]!;
    const y = b[i]!;
    if (x.file_id !== y.file_id || x.root_id !== y.root_id || x.path !== y.path) return false;
    if ((x.last_at ?? "") !== (y.last_at ?? "")) return false;
    if (x.changed_since_presented !== y.changed_since_presented) return false;
    if (x.unpresented_agent_effects !== y.unpresented_agent_effects) return false;
    if ((x.presentation_effect_id ?? "") !== (y.presentation_effect_id ?? "")) {
      return false;
    }
    if ((x.presentation_ordinal ?? -1) !== (y.presentation_ordinal ?? -1)) return false;
    if (!sameTip(x.tip, y.tip)) return false;
    if (JSON.stringify(x.commit) !== JSON.stringify(y.commit) || x.head_match !== y.head_match) return false;
    if (!sameEffectRows(x.effects, y.effects)) return false;
  }
  return true;
}

function sameMarks(a: ResolvedScope, b: ResolvedScope): boolean {
  return (
    sameKeySet(a.paths, b.paths) &&
    sameKeySet(a.added, b.added) &&
    sameKeySet(a.deleted, b.deleted)
  );
}

function sameSubject(a: ScopeSubject | null, b: ScopeSubject | null): boolean {
  return a === b || (a?.sessionId === b?.sessionId && a?.title === b?.title && a?.sessionScoped === b?.sessionScoped);
}

/** Unchanged comparison content preserves tab targets. */
function sameContent(a: ResolvedScope, b: ResolvedScope): boolean {
  return (
    a.request === b.request &&
    a.baseline === b.baseline &&
    a.turn === b.turn &&
    a.settled === b.settled &&
    a.needsChat === b.needsChat &&
    a.comparisonOff === b.comparisonOff &&
    a.markUserEdits === b.markUserEdits &&
    a.commitAvailable === b.commitAvailable &&
    encodeEyeChoice(a.scope) === encodeEyeChoice(b.scope) &&
    (a.scope.kind !== "pin" || b.scope.kind !== "pin" || a.scope.label === b.scope.label) &&
    sameSubject(a.subject, b.subject) &&
    sameFiles(a.files, b.files) &&
    a.seenMore === b.seenMore &&
    sameSeen(a.seen, b.seen)
  );
}

function bump(map: Map<string, number>, projectId: string): void {
  map.set(projectId, (map.get(projectId) ?? 0) + 1);
}

function interchangeable(a: ResolvedScope, b: ResolvedScope): boolean {
  return a.status === b.status && a.error === b.error && sameContent(a, b);
}

/** Stores a record and notifies listeners only when visible scope state changes. */
function commit(projectId: string, next: ResolvedScope): ResolvedScope {
  const held = records.get(projectId);
  if (held && interchangeable(held, next)) return held;

  // Reuse settled records across no-op refreshes.
  const settled = settledRecords.get(projectId);
  const stored =
    next.status !== "resolving" && settled && interchangeable(settled, next)
      ? settled
      : next;

  if (!held || !sameMarks(held, stored)) bump(markEpochs, projectId);
  if (!held || !sameContent(held, stored)) bump(contentEpochs, projectId);
  records.set(projectId, stored);
  if (stored.status !== "resolving") settledRecords.set(projectId, stored);
  for (const fn of listeners) fn(projectId);
  return stored;
}

export function scopeMarksEpoch(projectId: string): number {
  return markEpochs.get(projectId.trim()) ?? 0;
}

export function scopeContentEpoch(projectId: string): number {
  return contentEpochs.get(projectId.trim()) ?? 0;
}

/** The claim behind the newest answer that landed. */
export function scopeSettledEpoch(projectId: string): number {
  return settledEpochs.get(projectId.trim()) ?? 0;
}

/** The newest resolve claimed; an answer from a later claim was read after now. */
export function scopeClaimedEpoch(projectId: string): number {
  return epochs.get(projectId.trim()) ?? 0;
}

/** Observes every answer that lands, including one identical to the last. */
export function subscribeScopeSettled(fn: (projectId: string) => void): () => void {
  settledListeners.add(fn);
  return () => settledListeners.delete(fn);
}

function settle(projectId: string, epoch: number): void {
  if ((settledEpochs.get(projectId) ?? 0) >= epoch) return;
  settledEpochs.set(projectId, epoch);
  for (const fn of settledListeners) fn(projectId);
}

export function subscribeResolvedScope(
  fn: (projectId: string) => void,
): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function resolvedScope(projectId: string): ResolvedScope {
  return records.get(projectId.trim()) ?? emptyRecord();
}

/** What a resolve asks for; the title is copy, and a shared checkout needs no chat. */
function requestKey(
  scope: ReviewLensScope,
  subject: ScopeSubject | null,
  markUserEdits: boolean,
  off: boolean,
): string {
  return JSON.stringify([off ? "off" : encodeEyeChoice(scope), subjectAddress(scope, subject) ?? "", markUserEdits]);
}

function answerWithoutFiles(
  scope: ReviewLensScope,
  subject: ScopeSubject | null,
  request: string,
  markUserEdits: boolean,
  off: boolean,
): ResolvedScope {
  return {
    scope,
    subject,
    request,
    baseline: "",
    turn: null,
    status: "ready",
    error: null,
    settled: true,
    needsChat: !off,
    comparisonOff: off,
    markUserEdits,
    commitAvailable: false,
    files: [],
    paths: new Set(),
    effects: new Map(),
    deleted: new Set(),
    added: new Set(),
    ...NO_SEEN,
  };
}

/** Publishes the comparison atomically if its request is still current. */
export async function resolveScope(
  projectId: string,
  client: LycaonClient | null | undefined,
  subject: ScopeSubject | null,
): Promise<ResolvedScope> {
  const id = projectId.trim();
  if (!id) return emptyRecord();

  const scope = getSidebarScope(id);
  const off = isComparisonOff(id);
  const markUserEdits = scope.kind === "commit" || getMarkMyEdits(id);
  const request = requestKey(scope, subject, markUserEdits, off);

  const claim = () => {
    const next = (epochs.get(id) ?? 0) + 1;
    epochs.set(id, next);
    return next;
  };

  const wanted = requestBaseline(scope, subject);
  if (off || wanted === null) {
    const epoch = claim();
    const stored = commit(id, answerWithoutFiles(scope, subject, request, markUserEdits, off));
    settle(id, epoch);
    return stored;
  }

  if (!client) {
    return resolvedScope(id);
  }

  const epoch = claim();
  // A new question holds the last answer, labeled as what it answered, until its own lands.
  const current = resolvedScope(id);
  if (!current.settled || current.request !== request) {
    commit(id, { ...current, status: "resolving", error: null });
  }

  // Seen loads beside the new files; any failed Seen read keeps the list shown.
  const seenPage: Promise<SourceSeenList | undefined> | null =
    scope.kind === "new"
      ? Promise.resolve()
          .then(() =>
            client.listProjectSourceSeen(id, {
              limit: getSeenLimit(id),
              ...(markUserEdits ? {} : { markUserEdits: false }),
              sessionId: subjectAddress(scope, subject),
            }),
          )
          .catch(() => undefined)
      : null;

  try {
    // Later pages send the baseline the first page resolved, so every page reads one range.
    let baseline = wanted;
    let answered: string | null = null;
    let cursor: string | undefined;
    let rootError: string | null = null;
    let rootSnapshot: string | undefined;
    let commitAvailable = false;
    const files: SourceWalkFile[] = [];
    for (;;) {
      const page = await client.listProjectSourceWalk(id, {
        baseline,
        limit: FILE_LIMIT,
        cursor,
        ...(markUserEdits ? {} : { markUserEdits: false }),
        sessionId: subjectAddress(scope, subject),
      });
      if (epochs.get(id) !== epoch) return resolvedScope(id);
      answered ??= page.baseline;
      baseline = answered;
      commitAvailable ||= page.commit_available;
      if (page.commit_roots) {
        const snapshot = JSON.stringify(page.commit_roots);
        if (rootSnapshot !== undefined && rootSnapshot !== snapshot) throw new Error("Git changed while loading. Refresh the comparison.");
        rootSnapshot = snapshot;
        const failures = page.commit_roots.filter((root) => !root.available);
        if (failures.length) rootError = `${failures.length} folder${failures.length === 1 ? "" : "s"} could not be compared with Git. This list is incomplete.`;
      }
      files.push(...page.files);
      const next = page.next_cursor;
      if (!next) break;
      cursor = next;
    }
    const mergedFiles = mergeSourceWalkFiles(files);
    const { paths, effects, deleted, added } = indexFiles(mergedFiles);
    const seenList = seenPage ? await seenPage : null;
    if (epochs.get(id) !== epoch) return resolvedScope(id);
    const held = resolvedScope(id);
    const seen = seenList
      ? seenOf(seenList, mergedFiles)
      : seenPage && held.request === request
        ? { seen: held.seen, seenMore: held.seenMore }
        : NO_SEEN;
    if (rootError) reportSurfaceFailure(SCOPE_UNAVAILABLE, rootError, id);
    const stored = commit(id, {
      scope,
      subject,
      request,
      baseline,
      turn: scope.kind === "turn" ? baselineTurn(baseline) : null,
      status: "ready",
      error: rootError,
      settled: true,
      needsChat: false,
      comparisonOff: false,
      markUserEdits,
      commitAvailable,
      files: mergedFiles,
      paths,
      effects,
      deleted,
      added,
      ...seen,
    });
    settle(id, epoch);
    return stored;
  } catch (err) {
    if (epochs.get(id) !== epoch) return resolvedScope(id);
    const held = resolvedScope(id);
    const message = err instanceof Error ? err.message : "Failed to load changes";
    reportSurfaceFailure(SCOPE_UNAVAILABLE, err instanceof Error ? err : message, id);
    return commit(id, { ...held, status: "error", error: message });
  }
}

export function isInScope(
  projectId: string,
  rootId: string,
  path: string,
): boolean {
  return resolvedScope(projectId).paths.has(fileKey(rootId, path));
}

export function isDeletedInScope(
  projectId: string,
  rootId: string,
  path: string,
): boolean {
  return resolvedScope(projectId).deleted.has(fileKey(rootId, path));
}

export function isAddedInScope(
  projectId: string,
  rootId: string,
  path: string,
): boolean {
  return resolvedScope(projectId).added.has(fileKey(rootId, path));
}

export function scopeChangeKindFor(
  projectId: string,
  rootId: string,
  path: string,
): ScopeChangeKind | null {
  return scopeChangeKindFromFlags({
    inScope: isInScope(projectId, rootId, path),
    added: isAddedInScope(projectId, rootId, path),
    deleted: isDeletedInScope(projectId, rootId, path),
  });
}

export function deletedPathsInScope(
  projectId: string,
  rootId: string,
): string[] {
  const prefix = `${rootId}\0`;
  const out: string[] = [];
  for (const key of resolvedScope(projectId).deleted) {
    if (key.startsWith(prefix)) out.push(key.slice(prefix.length));
  }
  return out;
}

export function scopeEffectFor(
  projectId: string,
  rootId: string,
  path: string,
): ScopeEffect | null {
  return resolvedScope(projectId).effects.get(fileKey(rootId, path)) ?? null;
}

export function scopeDiffRevision(
  projectId: string,
  rootId: string,
  path: string,
): string {
  const record = resolvedScope(projectId);
  const effect = scopeEffectFor(projectId, rootId, path);
  return [
    "range",
    record.baseline,
    record.markUserEdits ? "marked" : "unmarked",
    effect?.fileId ?? "",
    effect?.entryOp ?? "",
    effect?.entryEffectId ?? "",
    effect?.tip.state ?? "",
    effect?.tip.sha256 ?? "",
    effect?.commit?.head ?? "",
  ].join("\0");
}

export function scopeFileFor(
  projectId: string,
  rootId: string,
  path: string,
): SourceWalkFile | null {
  return (
    resolvedScope(projectId).files.find(
      (f) => f.root_id === rootId && f.path === path,
    ) ?? null
  );
}

/** How the resolved answer names itself: the comparison it read, not the one being picked. */
export function resolvedLensView(projectId: string): {
  scope: ReviewLensScope;
  comparisonOff: boolean;
  subjectTitle: string;
} {
  const record = resolvedScope(projectId);
  const settled = record.status !== "empty";
  return {
    scope: settled ? record.scope : getSidebarScope(projectId),
    comparisonOff: settled ? record.comparisonOff : isComparisonOff(projectId),
    subjectTitle: record.subject?.title ?? "",
  };
}

/** Follows the chat's name or checkout on the held answer; a chat it never addressed is refused. */
export function rebindScopeSubject(projectId: string, subject: ScopeSubject | null): void {
  const id = projectId.trim();
  const held = records.get(id);
  if (!held || held.status === "empty" || sameSubject(held.subject, subject)) return;
  if (subjectAddress(held.scope, subject) !== subjectAddress(held.scope, held.subject)) return;
  commit(id, { ...held, subject });
}

export function scopeBaseline(projectId: string): string {
  return resolvedScope(projectId).baseline;
}

/** The chat the resolved answer addressed, so per-file reads of it use that same workspace. */
export function scopeSessionId(projectId: string): string | undefined {
  const record = resolvedScope(projectId);
  return subjectAddress(record.scope, record.subject);
}

export function scopeComparisonTarget(projectId: string, rootId: string, path: string): SourceRangeTarget {
  const change = scopeEffectFor(projectId, rootId, path);
  const record = resolvedScope(projectId);
  return record.baseline === "commit"
    ? { rootId, path, baseline: "commit", expectedHead: change?.commit?.head }
    : {
        fileId: change?.fileId ?? "",
        baseline: record.baseline,
        ...(record.markUserEdits ? {} : { markUserEdits: false }),
      };
}

export function setScopeResolver(
  projectId: string,
  resolve: (() => void) | null,
): void {
  const id = projectId.trim();
  if (!id) return;
  if (resolve) resolvers.set(id, resolve);
  else resolvers.delete(id);
}

/** Asks the project's resolver for a fresh answer; safe to call from any surface. */
export function requestScopeResolve(projectId: string): void {
  resolvers.get(projectId.trim())?.();
}

subscribeReviewScope(requestScopeResolve);

export function resetScopeResolutionForTests(): void {
  records.clear();
  epochs.clear();
  settledEpochs.clear();
  resolvers.clear();
  markEpochs.clear();
  contentEpochs.clear();
  settledRecords.clear();
}
