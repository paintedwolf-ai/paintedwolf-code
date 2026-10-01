import type { ProjectRoot, SourceTreeFrame, SourceTreeRow } from "../../api/types.ts";
import type { DisplayRow, DisplayRowModel } from "./files-tree-display-model.ts";
import type { FlatTreeRow } from "./project-files-tree-flat.ts";
import type { StickyAncestor } from "./files-tree-sticky.ts";
import type { NewEntryKind } from "../commands/project-files-create.ts";

export type TreeDraft = { rootId: string; dir: string; naming: NewEntryKind | null; createError: string | null };
export type PagedTreeModel = DisplayRowModel & {
  sourceIndex(index: number): number;
  displayIndex(index: number): number;
};

export function treeFramesCover(frames: readonly Pick<SourceTreeFrame, "span">[], start: number, end: number): boolean {
  if (end <= start) return true;
  let covered = start;
  for (const { span } of [...frames].sort((a, b) => a.span.start - b.span.start)) {
    if (span.start > covered) return false;
    covered = Math.max(covered, span.end);
    if (covered >= end) return true;
  }
  return covered >= end;
}

function treeEntry(row: SourceTreeRow, labels: Map<string, string>): FlatTreeRow {
  return { key: `${row.address.root_id}\0${row.address.path}`, rootId: row.address.root_id,
    rootLabel: labels.get(row.address.root_id) ?? "", path: row.address.path, name: row.name,
    depth: row.depth, isDir: row.kind === "directory", isRoot: row.address.path === ".",
    expanded: row.expanded, deleted: row.deleted };
}

/** A retained ancestor whose subtree may extend past the rows its frame still proves. */
export const UNKNOWN_SUBTREE_END = Number.MAX_SAFE_INTEGER;

/** Maps bounded frames into display coordinates. */
export function buildPagedTreeModel(roots: readonly ProjectRoot[], count: number,
  frames: readonly SourceTreeFrame[], drafts: readonly TreeDraft[] = []): PagedTreeModel {
  const source = new Map<number, SourceTreeRow>();
  const ends = new Map<number, number>();
  for (const frame of frames) {
    for (const ancestor of frame.ancestors) {
      source.set(ancestor.index, ancestor.row);
      // Exact subtree ends take precedence over unknown ends.
      ends.set(ancestor.index, Math.min(ends.get(ancestor.index) ?? UNKNOWN_SUBTREE_END, ancestor.end));
    }
    frame.rows.forEach((row, offset) => source.set(frame.span.start + offset, row));
  }
  if (roots.length === 1 && source.get(0)?.address.path === ".") ends.set(0, count);
  return buildTreeRowsModel(roots, count, source, ends, drafts);
}

/** Root metadata remains navigable before a host connection is available. */
export function buildKnownRootsModel(roots: readonly ProjectRoot[]): PagedTreeModel {
  return buildTreeRowsModel(roots, roots.length, new Map(roots.map((root, index) => [index, {
    address: { root_id: root.id, path: "." }, name: root.label, depth: 0, kind: "directory", expanded: false,
  }])), new Map(), []);
}

function buildTreeRowsModel(roots: readonly ProjectRoot[], count: number, source: Map<number, SourceTreeRow>,
  ends: Map<number, number>, drafts: readonly TreeDraft[]): PagedTreeModel {
  const labels = new Map(roots.map(root => [root.id, root.label]));
  const addresses = new Map<string, number>();
  for (const [index, row] of source) {
    if (row.kind === "directory" || row.kind === "file") addresses.set(`${row.address.root_id}\0${row.address.path}`, index);
  }
  const inserted: { at: number; rows: DisplayRow[] }[] = [];
  for (const draft of drafts) {
    const parent = addresses.get(`${draft.rootId}\0${draft.dir}`);
    if (parent === undefined) continue;
    const parentRow = source.get(parent);
    if (!parentRow) continue;
    const entry = treeEntry(parentRow, labels);
    const rows: DisplayRow[] = [];
    if (draft.naming) rows.push({ kind: "naming", key: `${entry.key}:naming`, rootId: draft.rootId,
      rootLabel: entry.rootLabel, dir: draft.dir, depth: entry.depth, entryKind: draft.naming });
    if (draft.createError) rows.push({ kind: "create-error", key: `${entry.key}:create-error`, depth: entry.depth + 1, message: draft.createError });
    if (rows.length) inserted.push({ at: parent + 1, rows });
  }
  inserted.sort((a, b) => a.at - b.at);
  const displayIndex = (index: number) => index + inserted.reduce((sum, item) => sum + (item.at <= index ? item.rows.length : 0), 0);
  const sourceIndex = (index: number) => {
    let removed = 0;
    for (const item of inserted) {
      if (index < item.at + removed) break;
      if (index < item.at + removed + item.rows.length) return item.at - 1;
      removed += item.rows.length;
    }
    return index - removed;
  };
  const rows = new Map<number, DisplayRow>();
  for (const [index, row] of source) {
    if (row.kind === "loading") continue;
    const key = `${row.address.root_id}\0${row.address.path}`;
    rows.set(displayIndex(index), row.kind === "directory" || row.kind === "file"
      ? { kind: "entry", key, entry: treeEntry(row, labels) }
      : row.kind === "error" ? { kind: "error", key: `${key}:error`, depth: row.depth, message: row.error ?? "Could not list files." }
      : { kind: row.kind, key: `${key}:${row.kind}`, depth: row.depth });
  }
  let added = 0;
  for (const item of inserted) {
    item.rows.forEach((row, offset) => rows.set(item.at + added + offset, row)); added += item.rows.length;
  }
  const length = count + added;
  const entryAt = (index: number) => { const row = rows.get(index); return row?.kind === "entry" ? row.entry : null; };
  const entryIndexes = [...rows.keys()].filter(index => entryAt(index)).sort((a, b) => a - b);
  // Loaded adjacent rows determine subtree ends without a full-tree walk.
  const stack: { index: number; entry: FlatTreeRow }[] = [];
  let previousIndex = -1;
  for (const index of entryIndexes) {
    if (index !== previousIndex + 1) stack.length = 0;
    previousIndex = index;
    const entry = entryAt(index);
    if (!entry) continue;
    while (stack.length) {
      const previous = stack[stack.length - 1];
      if (!previous || previous.entry.rootId === entry.rootId && previous.entry.depth < entry.depth) break;
      stack.pop();
      if (!ends.has(sourceIndex(previous.index))) ends.set(sourceIndex(previous.index), sourceIndex(index));
    }
    if (entry.isDir) stack.push({ index, entry });
  }
  const handles = new Map<number, StickyAncestor>();
  const ancestorAt = (index: number) => {
    const entry = entryAt(index); if (!entry) return null;
    let handle = handles.get(index);
    if (!handle) { handle = { displayIndex: index, entry }; handles.set(index, handle); }
    return handle;
  };
  const chains = new Map<string, StickyAncestor[]>();
  const chainAt = (index: number): StickyAncestor[] => {
    const sourceAt = sourceIndex(index);
    const sourceRow = source.get(sourceAt);
    if (!sourceRow) {
      // Host subtree bounds keep known ancestors stable across missing pages.
      return [...ends].filter(([start, end]) => start < sourceAt && sourceAt < end)
        .sort(([left], [right]) => left - right)
        .flatMap(([start]) => {
          const handle = ancestorAt(displayIndex(start));
          return handle?.entry.expanded ? [handle] : [];
        });
    }
    const path = sourceRow.address.path;
    if (path === "." && (sourceRow.kind === "directory" || sourceRow.kind === "file")) return [];
    const slash = path.lastIndexOf("/");
    const parent = sourceRow.kind === "directory" || sourceRow.kind === "file"
      ? slash < 0 ? "." : path.slice(0, slash) : path;
    // Sibling rows share one immutable ancestry chain within this model.
    const key = `${sourceRow.address.root_id}\0${parent}`;
    const cached = chains.get(key);
    if (cached) return cached;
    const paths = [parent];
    for (let dir = parent; dir !== ".";) {
      const slash = dir.lastIndexOf("/");
      dir = slash < 0 ? "." : dir.slice(0, slash);
      paths.push(dir);
    }
    const chain = paths.reverse().flatMap(parent => {
      const found = addresses.get(`${sourceRow.address.root_id}\0${parent}`);
      const handle = found === undefined ? null : ancestorAt(displayIndex(found));
      return handle ? [handle] : [];
    });
    chains.set(key, chain);
    return chain;
  };
  const rowAt = (index: number): DisplayRow | undefined => index < 0 || index >= length ? undefined : rows.get(index);
  const depthAt = (index: number) => { const row = rowAt(index); return !row ? -1 : row.kind === "entry" ? row.entry.depth : row.depth; };
  return { length, rowCount: length, sourceIndex, displayIndex, rowAt, entryAt, depthAt, ancestorAt, chainAt,
    nextOutsideAt: index => { const end = ends.get(sourceIndex(index)); return end === undefined || end >= count ? -1 : displayIndex(end); },
    indexOfEntry: (root, path, isDir) => { const index = addresses.get(`${root}\0${path}`); if (index === undefined) return -1;
      const display = displayIndex(index); return isDir === undefined || entryAt(display)?.isDir === isDir ? display : -1; },
    indexOfKey: key => { for (const [index, row] of rows) if (row.key === key) return index; return -1; },
    nextEntryIndex: (from, delta) => {
      for (let at = from + delta; at >= 0 && at < length; at += delta) {
        const row = rows.get(at); if (!row || row.kind === "entry") return at;
      }
      return -1;
    },
    firstEntryIndex: () => length ? 0 : -1,
    lastEntryIndex: () => length ? length - 1 : -1,
  };
}
