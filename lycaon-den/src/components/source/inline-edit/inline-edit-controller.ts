/**
 * Inline-edit panel state + Escape ladder slot. No host I/O here.
 */

import { createSignal } from "solid-js";
import { registerEscapeLadderLayer } from "../../../find/escape-ladder.ts";
import type {
  RenameProjectCount,
  RenameScope,
} from "./rename-card-model.ts";
import type { ResolvedInlineEditScope } from "./inline-edit-scope.ts";

/**
 * Rename opens on any identifier; symbolName is the word under the caret.
 * With no identifier there, symbolName is null and the card explains how to target one.
 */
export type InlineEditMode =
  | { kind: "inline" }
  | { kind: "rename"; symbolName: string | null };

export type InlineEditOpenState = {
  projectId: string;
  rootId: string;
  path: string;
  bufferKey: string;
  scope: ResolvedInlineEditScope;
  mode: InlineEditMode;
  /** Anchor coords relative to the editor host (overlay positioning). */
  anchor: { top: number; left: number; width: number };
};

const HISTORY_CAP = 10;

const [openState, setOpenState] = createSignal<InlineEditOpenState | null>(
  null,
);
const [instruction, setInstruction] = createSignal("");
const [running, setRunning] = createSignal(false);
const [progressNoun, setProgressNoun] = createSignal("Rewriting selection…");
const [error, setError] = createSignal<string | null>(null);
const [renameScope, setRenameScopeSignal] = createSignal<RenameScope>("file");
const [renameInFileCount, setRenameInFileCount] = createSignal(0);
const [renameProjectCount, setRenameProjectCountSignal] =
  createSignal<RenameProjectCount | null>(null);
const [renameCountPending, setRenameCountPending] = createSignal(false);

let history: string[] = [];
let historyCursor = -1;
let historyAnchor: string | null = null;

export function inlineEditOpen() {
  return openState();
}

export function inlineEditInstruction() {
  return instruction();
}

export function inlineEditRunning() {
  return running();
}

export function inlineEditProgressNoun() {
  return progressNoun();
}

export function inlineEditError() {
  return error();
}

export function inlineEditRenameScope() {
  return renameScope();
}

export function setInlineEditRenameScope(scope: RenameScope) {
  setRenameScopeSignal(scope);
}

export function inlineEditRenameInFileCount() {
  return renameInFileCount();
}

export function inlineEditRenameProjectCount() {
  return renameProjectCount();
}

export function inlineEditRenameCountPending() {
  return renameCountPending();
}

/** Set at open (buffer scan) and when the async project count resolves. */
export function setInlineEditRenameCounts(args: {
  inFile?: number;
  project?: RenameProjectCount | null;
  pending?: boolean;
}) {
  if (args.inFile !== undefined) setRenameInFileCount(args.inFile);
  if (args.project !== undefined) setRenameProjectCountSignal(args.project);
  if (args.pending !== undefined) setRenameCountPending(args.pending);
}

export function setInlineEditInstruction(value: string) {
  setInstruction(value);
  historyCursor = -1;
  historyAnchor = null;
}

function isSameRenameTarget(
  current: InlineEditOpenState,
  next: InlineEditOpenState,
): boolean {
  return (
    current.mode.kind === "rename" &&
    next.mode.kind === "rename" &&
    current.projectId === next.projectId &&
    current.rootId === next.rootId &&
    current.path === next.path &&
    current.bufferKey === next.bufferKey &&
    current.mode.symbolName === next.mode.symbolName &&
    current.scope.kind === next.scope.kind &&
    current.scope.startLine === next.scope.startLine &&
    current.scope.endLine === next.scope.endLine &&
    current.scope.symbolName === next.scope.symbolName
  );
}

/** Open a card; repeated rename for the same target preserves the live card. */
export function openInlineEdit(state: InlineEditOpenState): boolean {
  const current = openState();
  if (current && isSameRenameTarget(current, state)) return false;
  setOpenState(state);
  setInstruction("");
  setRunning(false);
  setError(null);
  setProgressNoun(
    state.mode.kind === "rename"
      ? "Renaming symbol…"
      : "Rewriting selection…",
  );
  setRenameScopeSignal("file");
  setRenameInFileCount(0);
  setRenameProjectCountSignal(null);
  setRenameCountPending(false);
  historyCursor = -1;
  historyAnchor = null;
  return true;
}

export function closeInlineEdit() {
  if (running()) return;
  setOpenState(null);
  setInstruction("");
  setError(null);
  historyCursor = -1;
  historyAnchor = null;
}

/** Force-close even while running (cancel). */
export function cancelInlineEdit() {
  setRunning(false);
  setOpenState(null);
  setInstruction("");
  setError(null);
  historyCursor = -1;
  historyAnchor = null;
}

export function setInlineEditRunning(value: boolean, noun?: string) {
  setRunning(value);
  if (noun) setProgressNoun(noun);
  if (!value) setError(null);
}

export function setInlineEditError(message: string | null) {
  setError(message);
  setRunning(false);
}

export function pushInlineEditHistory(text: string) {
  const t = text.trim();
  if (!t) return;
  history = [t, ...history.filter((h) => h !== t)].slice(0, HISTORY_CAP);
}

/** ArrowUp / ArrowDown through the last 10 instructions this session. */
export function walkInlineEditHistory(delta: -1 | 1): string | null {
  if (history.length === 0) return null;
  if (historyCursor < 0) {
    historyAnchor = instruction();
    historyCursor = 0;
    const v = history[0] ?? "";
    setInstruction(v);
    return v;
  }
  const next = historyCursor + delta;
  if (next < 0) {
    historyCursor = -1;
    const v = historyAnchor ?? "";
    historyAnchor = null;
    setInstruction(v);
    return v;
  }
  if (next >= history.length) return instruction();
  historyCursor = next;
  const v = history[next] ?? "";
  setInstruction(v);
  return v;
}

registerEscapeLadderLayer("inlineEdit", {
  isOpen: () => openState() != null,
  dismiss: () => {
    if (running()) cancelInlineEdit();
    else closeInlineEdit();
  },
});

export function resetInlineEditForTests() {
  setOpenState(null);
  setInstruction("");
  setRunning(false);
  setError(null);
  history = [];
  historyCursor = -1;
  historyAnchor = null;
}
