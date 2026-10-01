import { readEditorScrollPosition, setEditorScrollTop } from "./editor-scroll-position.ts";
import { EditorSelection } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { foldEffect, foldedRanges } from "@codemirror/language";
import type {
  DenEditorViewStateEntry,
  DenEditorViewStateStore,
} from "../../../../shared/app-state-types.ts";

export const EDITOR_VIEW_STATE_LRU = 200;

export function editorViewStateKey(rootId: string, path: string): string {
  return `${rootId}\u0000${path}`;
}

export function captureEditorViewState(
  view: EditorView,
  sha: string,
  now = Date.now(),
): DenEditorViewStateEntry | null {
  const trimmed = sha.trim();
  if (!trimmed) return null;
  const main = view.state.selection.main;
  const folds: { from: number; to: number }[] = [];
  foldedRanges(view.state).between(0, view.state.doc.length, (from, to) => {
    folds.push({ from, to });
  });
  return {
    sha: trimmed,
    cursor: { anchor: main.anchor, head: main.head },
    selections: view.state.selection.ranges.map(({ anchor, head }) => ({ anchor, head })),
    mainSelection: view.state.selection.mainIndex,
    scrollTop: readEditorScrollPosition(view).top,
    folds,
    capturedAt: now,
  };
}

/** Saved positions apply only to matching file revisions. */
export function restoreEditorViewState(
  view: EditorView,
  record: DenEditorViewStateEntry,
  sha: string | null | undefined,
): boolean {
  const want = (sha ?? "").trim();
  if (!want || want !== record.sha) return false;
  const docLen = view.state.doc.length;
  const { anchor, head } = record.cursor;
  if (
    !Number.isFinite(anchor) ||
    !Number.isFinite(head) ||
    anchor < 0 ||
    head < 0 ||
    anchor > docLen ||
    head > docLen
  ) {
    return false;
  }
  const foldEffects = record.folds
    .filter(
      (f) =>
        Number.isFinite(f.from) &&
        Number.isFinite(f.to) &&
        f.from >= 0 &&
        f.to > f.from &&
        f.to <= docLen,
    )
    .map((f) => foldEffect.of({ from: f.from, to: f.to }));
  view.dispatch({
    selection: EditorSelection.create(
      (record.selections ?? [{ anchor, head }]).map(range => EditorSelection.range(
        Math.max(0, Math.min(docLen, range.anchor)), Math.max(0, Math.min(docLen, range.head)))),
      record.mainSelection ?? 0),
    effects: foldEffects,
    userEvent: "select",
  });
  restoreEditorScrollTop(view, record.scrollTop);
  return true;
}

/** Scroll restoration waits for the first layout. */
export function restoreEditorScrollTop(
  view: EditorView,
  scrollTop: number,
): void {
  const top = Number.isFinite(scrollTop) ? Math.max(0, scrollTop) : 0;
  view.requestMeasure({
    read: () => null,
    write: () => {
      setEditorScrollTop(view, top);
    },
  });
}

export class EditorViewStateMap {
  private entries = new Map<string, DenEditorViewStateEntry>();

  clear(): void {
    this.entries.clear();
  }

  get(key: string): DenEditorViewStateEntry | undefined {
    return this.entries.get(key);
  }

  set(key: string, record: DenEditorViewStateEntry): void {
    this.entries.set(key, record);
  }

  delete(key: string): void {
    this.entries.delete(key);
  }

  /** File and directory renames update matching view-state keys. */
  rekeyUnderPath(
    rootId: string,
    fromPath: string,
    toPath: string,
  ): void {
    const moves: { from: string; to: string }[] = [];
    const prefix = `${rootId}\u0000`;
    for (const key of this.entries.keys()) {
      if (!key.startsWith(prefix)) continue;
      const path = key.slice(prefix.length);
      let nextPath: string | null = null;
      if (path === fromPath) nextPath = toPath;
      else if (fromPath !== "." && path.startsWith(`${fromPath}/`)) {
        nextPath = `${toPath}${path.slice(fromPath.length)}`;
      }
      if (nextPath == null) continue;
      moves.push({ from: key, to: editorViewStateKey(rootId, nextPath) });
    }
    for (const m of moves) {
      const entry = this.entries.get(m.from);
      if (!entry) continue;
      this.entries.delete(m.from);
      this.entries.set(m.to, entry);
    }
  }

  /** Serialization retains the most recently captured entries. */
  toStore(cap = EDITOR_VIEW_STATE_LRU, retained: ReadonlySet<string> = new Set()): DenEditorViewStateStore {
    const list = [...this.entries.entries()].sort(
      (a, b) => b[1].capturedAt - a[1].capturedAt,
    );
    const kept = [...list.filter(([key]) => retained.has(key)), ...list.filter(([key]) => !retained.has(key)).slice(0, cap)];
    const byKey: Record<string, DenEditorViewStateEntry> = {};
    for (const [k, v] of kept) byKey[k] = v;
    // In-memory entries follow persisted eviction.
    if (kept.length < this.entries.size) {
      this.entries.clear();
      for (const [k, v] of kept) this.entries.set(k, v);
    }
    return { byKey };
  }

  loadStore(store: DenEditorViewStateStore | undefined): void {
    this.entries.clear();
    if (!store?.byKey) return;
    for (const [k, v] of Object.entries(store.byKey)) {
      const parsed = parseViewStateEntry(v);
      if (parsed) this.entries.set(k, parsed);
    }
  }

  size(): number {
    return this.entries.size;
  }
}

export function parseViewStateEntry(
  value: unknown,
): DenEditorViewStateEntry | null {
  if (typeof value !== "object" || value === null) return null;
  const row = value as Record<string, unknown>;
  if (typeof row.sha !== "string" || !row.sha.trim()) return null;
  const cursor = row.cursor;
  if (typeof cursor !== "object" || cursor === null) return null;
  const c = cursor as Record<string, unknown>;
  if (typeof c.anchor !== "number" || typeof c.head !== "number") return null;
  if (typeof row.scrollTop !== "number" || !Number.isFinite(row.scrollTop)) {
    return null;
  }
  if (typeof row.capturedAt !== "number" || !Number.isFinite(row.capturedAt)) {
    return null;
  }
  const foldsRaw = Array.isArray(row.folds) ? row.folds : [];
  const folds: { from: number; to: number }[] = [];
  for (const f of foldsRaw) {
    if (typeof f !== "object" || f === null) continue;
    const fr = f as Record<string, unknown>;
    if (typeof fr.from !== "number" || typeof fr.to !== "number") continue;
    folds.push({ from: fr.from, to: fr.to });
  }
  return {
    sha: row.sha.trim(),
    cursor: { anchor: c.anchor, head: c.head },
    ...(validSelections(row.selections) ? { selections: row.selections, mainSelection: typeof row.mainSelection === "number" && Number.isSafeInteger(row.mainSelection) ? Math.max(0, Math.min(row.selections.length - 1, Math.floor(row.mainSelection))) : 0 } : {}),
    ...(validDocumentPositions(row.document) ? { document: row.document } : {}),
    ...(validReaderState(row.reader) ? { reader: row.reader } : {}),
    scrollTop: row.scrollTop,
    folds,
    capturedAt: row.capturedAt,
  };
}

export function parseEditorViewStateStore(
  value: unknown,
): DenEditorViewStateStore | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const row = value as { byKey?: unknown };
  if (typeof row.byKey !== "object" || row.byKey === null) return undefined;
  const byKey: Record<string, DenEditorViewStateEntry> = {};
  for (const [k, v] of Object.entries(row.byKey as Record<string, unknown>)) {
    if (typeof k !== "string" || !k.includes("\u0000")) continue;
    const entry = parseViewStateEntry(v);
    if (entry) byKey[k] = entry;
  }
  // Open tabs are retained independently of the closed-file LRU at serialization.
  return { byKey };
}

function validSelections(value: unknown): value is Array<{ anchor: number; head: number }> {
  return Array.isArray(value) && value.length > 0 && value.length <= 256 && value.every(item =>
    item && Number.isSafeInteger(item.anchor) && item.anchor >= 0 && Number.isSafeInteger(item.head) && item.head >= 0);
}

function validDocumentPositions(value: unknown): value is NonNullable<DenEditorViewStateEntry["document"]> {
  if (!value || typeof value !== "object") return false;
  const anchor = (v: unknown) => typeof v === "string" && v.length > 0 && v.length <= 1024;
  return "id" in value && typeof value.id === "string" && value.id.length <= 256
    && "epoch" in value && typeof value.epoch === "number" && Number.isSafeInteger(value.epoch) && value.epoch > 0
    && "selections" in value && Array.isArray(value.selections) && value.selections.length > 0 && value.selections.length <= 256
    && value.selections.every(p => p && anchor(p.anchor) && anchor(p.head))
    && "folds" in value && Array.isArray(value.folds) && value.folds.length <= 10000
    && value.folds.every(p => p && anchor(p.from) && anchor(p.to));
}

function validReaderState(value: unknown): value is NonNullable<DenEditorViewStateEntry["reader"]> {
  if (!value || typeof value !== "object") return false;
  const coordinate = (n: unknown) => typeof n === "number" && Number.isSafeInteger(n) && n >= 0;
  const boundary = (b: unknown): boolean => !!b && typeof b === "object" && "row" in b && coordinate(b.row)
    && "offset" in b && coordinate(b.offset) && "side" in b && (b.side === "before" || b.side === "after");
  if ("selection" in value && value.selection !== undefined) {
    const s = value.selection;
    if (!s || typeof s !== "object" || !("ranges" in s) || !Array.isArray(s.ranges) || s.ranges.length > 256
      || !("main" in s) || !coordinate(s.main) || typeof s.main !== "number" || s.main >= Math.max(1, s.ranges.length)
      || !s.ranges.every(r => r && boundary(r.anchor) && boundary(r.head))) return false;
  }
  if ("viewport" in value && value.viewport !== undefined) {
    const v = value.viewport;
    if (!v || typeof v !== "object" || !("rank" in v) || !coordinate(v.rank) || !("fraction" in v)
      || typeof v.fraction !== "number" || !Number.isFinite(v.fraction)) return false;
    if ("atEnd" in v && v.atEnd !== undefined && typeof v.atEnd !== "boolean") return false;
  }
  return true;
}
