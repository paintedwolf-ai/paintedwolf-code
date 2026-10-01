import { diffArrays } from "diff";
/** Playback animates changes after the replacement bytes have settled. */

import {
  Annotation,
  EditorSelection,
  EditorState,
  type Extension,
  StateEffect,
  StateField,
} from "@codemirror/state";
import {
  Decoration,
  EditorView,
  type DecorationSet,
} from "@codemirror/view";

export const PLAYBACK_EVENT_BUDGET_MS = 800;
export const PLAYBACK_BURST_BUDGET_MS = 2000;
export const TYPING_DEFER_MS = 2000;
export const DELETE_COLLAPSE_MS = 150;
export const INSERT_FADE_MS = 600;

export type LineHunk =
  | {
      kind: "equal";
      beforeStart: number;
      beforeEnd: number;
      afterStart: number;
      afterEnd: number;
    }
  | {
      kind: "delete";
      beforeStart: number;
      beforeEnd: number;
      afterStart: number;
      afterEnd: number;
    }
  | {
      kind: "insert";
      beforeStart: number;
      beforeEnd: number;
      afterStart: number;
      afterEnd: number;
    };

export function splitLines(text: string): string[] {
  if (text === "") return [];
  const endsWithNl = text.endsWith("\n");
  const body = endsWithNl ? text.slice(0, -1) : text;
  if (body === "") return endsWithNl ? [""] : [];
  return body.split("\n");
}

/** Bounded line diff for presentation; a wide rewrite may use coarse hunks. */
export function computeLineHunks(before: string, after: string): LineHunk[] {
  const a = splitLines(before), b = splitLines(after);
  const changes = diffArrays(a, b, { maxEditLength: 2000, timeout: 25 }) ?? [
    { removed: true, added: false, value: a }, { removed: false, added: true, value: b },
  ];
  const hunks: LineHunk[] = [];
  let oldLine = 0, newLine = 0;
  for (const change of changes) {
    if (change.value.length === 0) continue;
    const beforeEnd = oldLine + (change.added ? 0 : change.value.length);
    const afterEnd = newLine + (change.removed ? 0 : change.value.length);
    hunks.push({ kind: change.added ? "insert" : change.removed ? "delete" : "equal",
      beforeStart: oldLine, beforeEnd, afterStart: newLine, afterEnd });
    oldLine = beforeEnd; newLine = afterEnd;
  }
  return hunks;
}

export function settledContent(_before: string, after: string): string {
  return after;
}

export type PlaybackCursor = { line: number; col: number };

export type PlayHunksOptions = {
  reducedMotion?: boolean;
  lastTypedAt?: number;
  now?: number;
  eventBudgetMs?: number;
  burstRemainingMs?: number;
  onSettled: (after: string, cursor: PlaybackCursor) => void;
  /** Rechecks settlement eligibility before document replacement. */
  canSettle?: () => boolean;
  /** Called when a newer local edit makes the pending replacement stale. */
  onDiscarded?: () => void;
  visible?: boolean;
};

export type PlaybackSession = {
  cancel: () => void;
  done: Promise<void>;
};

export const playbackSettleAnnotation = Annotation.define<boolean>();

const setPlaybackDeco = StateEffect.define<DecorationSet>();
const playbackDecoField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(deco, tr) {
    for (const e of tr.effects) {
      if (e.is(setPlaybackDeco)) return e.value;
    }
    return deco.map(tr.changes);
  },
  provide: (f) => EditorView.decorations.from(f),
});

export const hunkPlaybackExtension: Extension = playbackDecoField;

function clampCursor(doc: EditorState["doc"], cursor: PlaybackCursor): PlaybackCursor {
  const lines = Math.max(doc.lines, 1);
  const line = Math.min(Math.max(cursor.line, 1), lines);
  const text = doc.line(line).text;
  const col = Math.min(Math.max(cursor.col, 1), text.length + 1);
  return { line, col };
}

/** Cursor offset in `text`, clamping line and column the way the doc would. */
function offsetForCursor(text: string, cursor: PlaybackCursor): number {
  let start = 0;
  for (let line = 1; line < Math.max(cursor.line, 1); line++) {
    const nl = text.indexOf("\n", start);
    if (nl < 0) break;
    start = nl + 1;
  }
  const nl = text.indexOf("\n", start);
  const end = nl < 0 ? text.length : nl;
  return Math.min(start + Math.max(cursor.col, 1) - 1, end);
}

const COMPARE_BLOCK = 4096;

function isLowSurrogate(code: number): boolean {
  return code >= 0xdc00 && code <= 0xdfff;
}

/** Replace only differing text to preserve the viewport and limit reparsing. */
export function settleChange(
  doc: EditorState["doc"],
  after: string,
): { from: number; to: number; insert: string } {
  const max = Math.min(doc.length, after.length);
  let prefix = 0;
  while (prefix < max) {
    const end = Math.min(prefix + COMPARE_BLOCK, max);
    const a = doc.sliceString(prefix, end);
    const b = after.slice(prefix, end);
    if (a === b) {
      prefix = end;
      continue;
    }
    let k = 0;
    while (a.charCodeAt(k) === b.charCodeAt(k)) k++;
    prefix += k;
    break;
  }

  let suffix = 0;
  const suffixMax = max - prefix;
  while (suffix < suffixMax) {
    const size = Math.min(COMPARE_BLOCK, suffixMax - suffix);
    const a = doc.sliceString(doc.length - suffix - size, doc.length - suffix);
    const b = after.slice(after.length - suffix - size, after.length - suffix);
    if (a === b) {
      suffix += size;
      continue;
    }
    let k = 0;
    while (a.charCodeAt(a.length - 1 - k) === b.charCodeAt(b.length - 1 - k)) k++;
    suffix += k;
    break;
  }

  // Preserve surrogate pairs at the replacement boundary.
  if (prefix > 0 && isLowSurrogate(after.charCodeAt(prefix))) prefix--;
  if (suffix > 0 && isLowSurrogate(after.charCodeAt(after.length - suffix))) {
    suffix--;
  }

  return {
    from: prefix,
    to: doc.length - suffix,
    insert: after.slice(prefix, after.length - suffix),
  };
}

export function applySettledDoc(
  view: EditorView,
  after: string,
  cursor: PlaybackCursor,
): void {
  view.dispatch({
    changes: settleChange(view.state.doc, after),
    selection: EditorSelection.cursor(offsetForCursor(after, cursor)),
    effects: setPlaybackDeco.of(Decoration.none),
    annotations: playbackSettleAnnotation.of(true),
  });
}

function readCursor(view: EditorView): PlaybackCursor {
  const head = view.state.selection.main.head;
  const line = view.state.doc.lineAt(head);
  return { line: line.number, col: head - line.from + 1 };
}

function lineDecorations(
  state: EditorState,
  start0: number,
  end0: number,
  className: string,
): DecorationSet {
  const ranges = [];
  const last = Math.max(state.doc.lines, 1);
  for (let line0 = start0; line0 < end0; line0++) {
    const lineNo = Math.min(Math.max(line0 + 1, 1), last);
    ranges.push(Decoration.line({ class: className }).range(state.doc.line(lineNo).from));
  }
  return Decoration.set(ranges, true);
}

export function playHunks(
  view: EditorView,
  before: string,
  after: string,
  opts: PlayHunksOptions,
): PlaybackSession {
  let cancelled = false;
  const cursor = readCursor(view);
  const now = opts.now ?? Date.now();
  const eventBudget = opts.eventBudgetMs ?? PLAYBACK_EVENT_BUDGET_MS;
  const burstLeft = opts.burstRemainingMs ?? PLAYBACK_BURST_BUDGET_MS;

  if (before === after) {
    return { cancel: () => { cancelled = true; }, done: Promise.resolve() };
  }
  if (opts.canSettle && !opts.canSettle()) {
    opts.onDiscarded?.();
    return { cancel: () => { cancelled = true; }, done: Promise.resolve() };
  }
  const settled = settledContent(before, after);
  applySettledDoc(view, settled, cursor);
  opts.onSettled(settled, clampCursor(view.state.doc, cursor));

  const typingDefer =
    opts.lastTypedAt != null && now - opts.lastTypedAt < TYPING_DEFER_MS;
  const skipAnim =
    opts.reducedMotion === true ||
    opts.visible === false ||
    typingDefer ||
    burstLeft <= 0;

  if (skipAnim) {
    return { cancel: () => { cancelled = true; }, done: Promise.resolve() };
  }

  const hunks = computeLineHunks(before, after).filter(
    (h) => h.kind === "delete" || h.kind === "insert",
  );

  let resolveDone!: () => void;
  const done = new Promise<void>((r) => {
    resolveDone = r;
  });
  const started = now;
  let step = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const clearTimer = () => {
    if (timer != null) clearTimeout(timer);
    timer = undefined;
  };

  const finish = () => {
    clearTimer();
    if (!cancelled) view.dispatch({ effects: setPlaybackDeco.of(Decoration.none) });
    resolveDone();
  };

  const runStep = () => {
    if (cancelled) {
      clearTimer();
      resolveDone();
      return;
    }
    const elapsed = Date.now() - started;
    if (elapsed >= eventBudget || elapsed >= burstLeft || step >= hunks.length) {
      finish();
      return;
    }
    const hunk = hunks[step]!;
    step++;
    if (hunk.kind === "delete") {
      const deco = lineDecorations(
        view.state,
        hunk.afterStart,
        hunk.afterStart + 1,
        "cm-den-hunk-del",
      );
      view.dispatch({ effects: setPlaybackDeco.of(deco) });
      timer = setTimeout(() => {
        if (cancelled) {
          resolveDone();
          return;
        }
        view.dispatch({ effects: setPlaybackDeco.of(Decoration.none) });
        runStep();
      }, DELETE_COLLAPSE_MS);
      return;
    }
    const deco = lineDecorations(
      view.state,
      hunk.afterStart,
      hunk.afterEnd,
      "cm-den-hunk-add",
    );
    view.dispatch({ effects: setPlaybackDeco.of(deco) });
    const wait = Math.min(
      INSERT_FADE_MS,
      Math.max(50, eventBudget - (Date.now() - started)),
    );
    timer = setTimeout(() => {
      if (cancelled) {
        resolveDone();
        return;
      }
      view.dispatch({ effects: setPlaybackDeco.of(Decoration.none) });
      runStep();
    }, wait);
  };

  runStep();

  return {
    cancel: () => {
      cancelled = true;
      clearTimer();
      view.dispatch({ effects: setPlaybackDeco.of(Decoration.none) });
      resolveDone();
    },
    done,
  };
}
