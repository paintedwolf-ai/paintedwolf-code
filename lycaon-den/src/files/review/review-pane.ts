import {
  type FilesStagePaneMode,
  encodeEyeChoice,
  parseEyeChoice,
  REVIEW_LENS_PROJECT_CAP,
  type ReviewLensScope,
} from "./review-model.ts";
import { leaveWalk } from "../walk/walk-store.ts";
import {
  getAppStateSnapshot,
  persistAppState,
} from "../../store/app-state-snapshot.ts";
import type { DeletedLines } from "../../components/source/diff/scope-diff.ts";

type PaneState = {
  mode: FilesStagePaneMode;
  scope: ReviewLensScope;
  /** Keeps the scope while suppressing comparison rendering. */
  comparisonOff: boolean;
  /** Off leaves the person's own edits unmarked. */
  markMyEdits: boolean;
  deletedLines: DeletedLines;
  /** Seen files the reader asked to list; grows a page at a time. */
  seenLimit: number;
};

/** One page of the Seen list. The host caps a request at 100. */
export const SEEN_PAGE = 25;
const SEEN_MAX = 100;

const byProject = new Map<string, PaneState>();

const modeListeners = new Set<(projectId: string) => void>();
const scopeListeners = new Set<(projectId: string) => void>();
const deletedLinesListeners = new Set<(projectId: string) => void>();

/** Observes only Files ↔ Review presentation changes. */
export function subscribeFilesStagePaneMode(
  fn: (projectId: string) => void,
): () => void {
  modeListeners.add(fn);
  return () => modeListeners.delete(fn);
}

/** Observes comparison changes that may require a new resolved scope. */
export function subscribeReviewScope(
  fn: (projectId: string) => void,
): () => void {
  scopeListeners.add(fn);
  return () => scopeListeners.delete(fn);
}

/** Observes how removed lines are shown; the comparison itself is unchanged. */
export function subscribeDeletedLines(
  fn: (projectId: string) => void,
): () => void {
  deletedLinesListeners.add(fn);
  return () => deletedLinesListeners.delete(fn);
}

function notify(listeners: Set<(projectId: string) => void>, projectId: string) {
  for (const fn of listeners) fn(projectId);
}

function ensure(projectId: string): PaneState {
  const id = projectId.trim();
  let s = byProject.get(id);
  if (!s) {
    s = {
      mode: "files",
      scope: { kind: "new" },
      comparisonOff: false,
      markMyEdits: false,
      deletedLines: "folded",
      seenLimit: SEEN_PAGE,
    };
    byProject.set(id, s);
  }
  return s;
}

function persistEyeState(projectId: string, state: PaneState): void {
  const byProject = {
    ...(getAppStateSnapshot().reviewLens?.byProject ?? {}),
    [projectId]: {
      comparison: encodeEyeChoice(state.scope),
      ...(state.comparisonOff ? { comparisonOff: true } : {}),
      ...(state.markMyEdits ? { markMyEdits: true } : {}),
      ...(state.deletedLines === "inplace" ? { deletedLines: "inplace" as const } : {}),
      touchedAt: Date.now(),
    },
  };
  const bounded = Object.fromEntries(
    Object.entries(byProject)
      .sort((a, b) => b[1].touchedAt - a[1].touchedAt)
      .slice(0, REVIEW_LENS_PROJECT_CAP),
  );
  void persistAppState({ reviewLens: { byProject: bounded } }).catch(
    () => undefined,
  );
}

export function getFilesStagePaneMode(projectId: string): FilesStagePaneMode {
  return ensure(projectId).mode;
}

export function setFilesStagePaneMode(
  projectId: string,
  mode: FilesStagePaneMode,
): void {
  const s = ensure(projectId);
  if (s.mode === mode) return;
  s.mode = mode;
  notify(modeListeners, projectId.trim());
}

export function getSidebarScope(projectId: string): ReviewLensScope {
  return ensure(projectId).scope;
}

/** Restores host presentation metadata without changing the selected comparison. */
export function hydrateReviewPinLabel(projectId: string, pinId: string, label: string): void {
  const id = projectId.trim();
  const state = byProject.get(id);
  if (state?.scope.kind !== "pin" || state.scope.pinId !== pinId) return;
  if (state.scope.label === label) return;
  state.scope = { ...state.scope, label };
  notify(scopeListeners, id);
}

export function isComparisonOff(projectId: string): boolean {
  return ensure(projectId).comparisonOff;
}

export function setComparisonOff(projectId: string, off: boolean): void {
  const id = projectId.trim();
  if (!id) return;
  const s = ensure(id);
  if (s.comparisonOff === off) return;
  s.comparisonOff = off;
  persistEyeState(id, s);
  notify(scopeListeners, id);
}

export function getMarkMyEdits(projectId: string): boolean {
  return ensure(projectId).markMyEdits;
}

/** Marks or unmarks the person's own edits everywhere the eye marks changes. */
export function setMarkMyEdits(projectId: string, on: boolean): void {
  const id = projectId.trim();
  if (!id) return;
  const s = ensure(id);
  if (s.markMyEdits === on) return;
  s.markMyEdits = on;
  persistEyeState(id, s);
  notify(scopeListeners, id);
}

export function getSeenLimit(projectId: string): number {
  return ensure(projectId).seenLimit;
}

/** Lists one more page of Seen files. */
export function showMoreSeen(projectId: string): void {
  const id = projectId.trim();
  if (!id) return;
  const s = ensure(id);
  const next = Math.min(s.seenLimit + SEEN_PAGE, SEEN_MAX);
  if (next === s.seenLimit) return;
  s.seenLimit = next;
  notify(scopeListeners, id);
}

export function getDeletedLines(projectId: string): DeletedLines {
  return ensure(projectId).deletedLines;
}

export function setDeletedLines(projectId: string, mode: DeletedLines): void {
  const id = projectId.trim();
  if (!id) return;
  const s = ensure(id);
  if (s.deletedLines === mode) return;
  s.deletedLines = mode;
  persistEyeState(id, s);
  notify(deletedLinesListeners, id);
}

/** Applies an explicit comparison scope. */
export function setSidebarScope(
  projectId: string,
  scope: ReviewLensScope,
): void {
  const id = projectId.trim();
  if (!id) return;
  const s = ensure(id);
  s.scope = scope;
  s.comparisonOff = false;
  persistEyeState(id, s);
  notify(scopeListeners, id);
}

/** Applies a picker choice and exits Walk. */
export function chooseSidebarScope(
  projectId: string,
  scope: ReviewLensScope,
): void {
  const id = projectId.trim();
  if (!id) return;
  leaveWalk(id);
  setSidebarScope(id, scope);
}

export const STANDARD_REVIEW_SCOPES: readonly ReviewLensScope[] = [
  { kind: "new" },
  { kind: "turn" },
  { kind: "session" },
  { kind: "commit" },
];

/** What the picker can offer right now: chat comparisons need a selected chat, Git needs a repository. */
export type ReviewScopeAvailability = {
  chatSelected: boolean;
  commitAvailable: boolean;
};

export function reviewScopeAvailable(scope: ReviewLensScope, availability: ReviewScopeAvailability): boolean {
  switch (scope.kind) {
    case "turn":
    case "session":
      return availability.chatSelected;
    case "commit":
      return availability.commitAvailable;
    default:
      return true;
  }
}

/** Toggles comparison on/off, preserving the active comparison scope. */
export function toggleComparison(projectId: string): boolean {
  const id = projectId.trim();
  if (!id) return false;
  const off = !isComparisonOff(id);
  if (off) {
    leaveWalk(id);
    setComparisonOff(id, true);
  } else {
    setComparisonOff(id, false);
  }
  return !off;
}

/** Cycles to the next or previous standard comparison the picker offers, skipping unavailable ones. */
export function cycleSidebarScope(
  projectId: string,
  direction: 1 | -1,
  availability: ReviewScopeAvailability,
): ReviewLensScope {
  const id = projectId.trim();
  const current = getSidebarScope(id);
  const off = isComparisonOff(id);
  if (off) {
    setComparisonOff(id, false);
    return current;
  }
  const count = STANDARD_REVIEW_SCOPES.length;
  const currentIndex = STANDARD_REVIEW_SCOPES.findIndex(
    (s) => s.kind === current.kind,
  );
  // An unlisted scope (a pin) enters the cycle at its near end.
  const start = currentIndex < 0 ? (direction > 0 ? -1 : count) : currentIndex;
  for (let step = 1; step <= count; step++) {
    const next = STANDARD_REVIEW_SCOPES[(((start + direction * step) % count) + count) % count]!;
    if (next.kind === current.kind) break;
    if (!reviewScopeAvailable(next, availability)) continue;
    chooseSidebarScope(id, next);
    return next;
  }
  return current;
}

/** Toggles deleted lines presentation between in-place and folded. */
export function toggleDeletedLines(projectId: string): DeletedLines {
  const id = projectId.trim();
  const current = getDeletedLines(id);
  const next: DeletedLines = current === "inplace" ? "folded" : "inplace";
  setDeletedLines(id, next);
  return next;
}

/** Loads persisted eye choices before Files surfaces mount. */
export function syncReviewPaneFromSnapshot(): void {
  byProject.clear();
  for (const [projectId, row] of Object.entries(
    getAppStateSnapshot().reviewLens?.byProject ?? {},
  )) {
    const id = projectId.trim();
    const scope = parseEyeChoice(row.comparison);
    if (!id || !scope) continue;
    byProject.set(id, {
      mode: "files",
      scope,
      comparisonOff: row.comparisonOff === true,
      markMyEdits: row.markMyEdits === true,
      deletedLines: row.deletedLines === "inplace" ? "inplace" : "folded",
      seenLimit: SEEN_PAGE,
    });
  }
}

const scopePickerListeners = new Set<(projectId: string) => void>();

/** Observes requests to open the comparison menu. */
export function subscribeScopePickerRequest(
  fn: (projectId: string) => void,
): () => void {
  scopePickerListeners.add(fn);
  return () => scopePickerListeners.delete(fn);
}

/** Asks the picker to open its comparison menu. */
export function requestScopePicker(projectId: string): void {
  notify(scopePickerListeners, projectId.trim());
}

/** Opens the Review tray with a scope. */
export function openReviewLens(
  projectId: string,
  scope: ReviewLensScope = { kind: "new" },
): void {
  const id = projectId.trim();
  if (!id) return;
  setSidebarScope(id, scope);
  setFilesStagePaneMode(id, "review");
}

export function resetFilesStagePaneForTests(): void {
  byProject.clear();
}
