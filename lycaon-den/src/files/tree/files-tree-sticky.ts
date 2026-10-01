import { DEN_FILES_TREE_LEVELS } from "../../../shared/app-state-types.ts";
import {
  FILES_TREE_ROW_HEIGHT_PX,
  type FlatTreeRow,
} from "./project-files-tree-flat.ts";

const FILES_TREE_STICKY_MAX_VIEWPORT_RATIO = 0.4;
/** Root pinning needs room for the elision and an inner folder. */
const FILES_TREE_STICKY_MIN_ITEMS_FOR_HEAD = 3;
/** Rows below the last slot whose folders already hold the lift. */
const FILES_TREE_STICKY_LOOKAHEAD_ROWS = 5;

/** Rows needed to compute a viewport before any of its content is published. */
export function treeLayoutRange(start: number, end: number, count: number) {
  return {
    start: Math.max(0, start - 2 * DEN_FILES_TREE_LEVELS.max - 1),
    end: Math.min(count, Math.max(end, start + DEN_FILES_TREE_LEVELS.max + FILES_TREE_STICKY_LOOKAHEAD_ROWS + 1)),
  };
}

export type StickyAncestor = {
  displayIndex: number;
  entry: FlatTreeRow;
};

export const STICKY_ELISION_KEY = "\0sticky-elision";

type StickyFolderSlot = {
  kind: "folder";
  key: string;
  ancestor: StickyAncestor;
  /** Moves with the stack below the pinned head. */
  lifted: boolean;
};

/** Stands in for the folders between the pinned root and the innermost folders. */
type StickyElisionSlot = {
  kind: "elision";
  key: typeof STICKY_ELISION_KEY;
  /** Root-first, including folders partially covered by this row. */
  hidden: readonly StickyAncestor[];
};

export type StickySlot = StickyFolderSlot | StickyElisionSlot;

type StickyStack = {
  slots: readonly StickySlot[];
  /** Viewport tops aligned with `slots`; later slots paint beneath earlier ones. */
  tops: readonly number[];
  /** Covered height above tree content. */
  height: number;
  /** Upward lift shared by lifted slots. */
  shift: number;
  /** Depth levels the elision replaces; rows below it indent by that many fewer. */
  fold: StickyFold;
};

/** Rows deeper than `floor` lose `levels` of indentation, never indenting shallower than `floor`. */
export type StickyFold = { floor: number; levels: number };

export function foldedTreeDepth(depth: number, fold: StickyFold): number {
  return Math.max(Math.min(depth, fold.floor), depth - fold.levels);
}

export const EMPTY_STICKY_SLOTS: readonly StickySlot[] = [];
export const NO_STICKY_FOLD: StickyFold = { floor: 0, levels: 0 };

const EMPTY_STICKY_STACK: StickyStack = { slots: EMPTY_STICKY_SLOTS, tops: [], height: 0, shift: 0, fold: NO_STICKY_FOLD };

export type StickyIndex = {
  readonly rowCount: number;
  entryAt(index: number): FlatTreeRow | null;
  /** Tree depth of a loaded row, or -1. */
  depthAt(index: number): number;
  /** Directory ancestors of the row at `index`, root-first, excluding itself. */
  chainAt(index: number): StickyAncestor[];
  ancestorAt(index: number): StickyAncestor | null;
  /** First display index after `index` outside that row's subtree, or -1. */
  nextOutsideAt(index: number): number;
};

function sameAncestor(left: StickyAncestor, right: StickyAncestor): boolean {
  if (left === right) return true;
  const a = left.entry;
  const b = right.entry;
  return left.displayIndex === right.displayIndex && a.key === b.key && a.depth === b.depth &&
    a.name === b.name && a.rootLabel === b.rootLabel && a.expanded === b.expanded && a.deleted === b.deleted;
}

function sameSlot(left: StickySlot, right: StickySlot): boolean {
  if (left === right) return true;
  if (left.kind === "folder") {
    return right.kind === "folder" && left.lifted === right.lifted && sameAncestor(left.ancestor, right.ancestor);
  }
  return right.kind === "elision" && left.hidden.length === right.hidden.length &&
    left.hidden.every((ancestor, index) => sameAncestor(ancestor, right.hidden[index]!));
}

/** Unchanged slots retain their DOM elements and bindings. */
export function retainStickySlots(previous: readonly StickySlot[], next: readonly StickySlot[]): readonly StickySlot[] {
  const retained = next.map(slot => previous.find(other => other.key === slot.key && sameSlot(other, slot)) ?? slot);
  return retained.length === previous.length && retained.every((slot, index) => slot === previous[index]) ? previous : retained;
}

/** Subtree boundaries determine pinned positions and folded indentation together. */
export function stickyStackFromIndex(
  index: StickyIndex,
  scrollTop: number,
  viewportHeight: number,
  maxItems: number,
  rowHeight: number = FILES_TREE_ROW_HEIGHT_PX,
): StickyStack {
  if (index.rowCount === 0 || viewportHeight <= 0 || rowHeight <= 0 || scrollTop <= 0) {
    return EMPTY_STICKY_STACK;
  }
  const maxByViewport = Math.floor((viewportHeight * FILES_TREE_STICKY_MAX_VIEWPORT_RATIO) / rowHeight);
  const cap = Math.min(maxItems, Math.max(0, maxByViewport));
  if (cap === 0) return EMPTY_STICKY_STACK;
  const firstRow = Math.floor(scrollTop / rowHeight);
  if (firstRow >= index.rowCount) return EMPTY_STICKY_STACK;

  const head = cap >= FILES_TREE_STICKY_MIN_ITEMS_FOR_HEAD ? 1 : 0;
  const lastSlotTop = (cap - 1) * rowHeight;
  const readingRow = Math.min(index.rowCount - 1, Math.floor((scrollTop + lastSlotTop) / rowHeight));
  const rowTop = (ancestor: StickyAncestor) => ancestor.displayIndex * rowHeight - scrollTop;
  const endTop = (ancestor: StickyAncestor) => {
    const end = index.nextOutsideAt(ancestor.displayIndex);
    return end >= 0 ? end * rowHeight - scrollTop : Infinity;
  };
  const expandedAt = (row: number) => {
    const entry = index.entryAt(row);
    return entry?.isDir === true && entry.expanded === true ? index.ancestorAt(row) : null;
  };

  // Lift ramps in near the last slot and decays at the subtree boundary.
  const lookahead = FILES_TREE_STICKY_LOOKAHEAD_ROWS * rowHeight;
  const demand = (depth: number, top: number, end: number) =>
    (depth - cap + 1) * rowHeight -
    Math.max(0, top - lastSlotTop - lookahead) -
    Math.max(0, cap * rowHeight - end);

  const candidates = new Map<number, StickyAncestor>();
  const readingSelf = expandedAt(readingRow);
  const reading = readingSelf ? [...index.chainAt(readingRow), readingSelf] : index.chainAt(readingRow);
  for (const ancestor of reading) candidates.set(ancestor.displayIndex, ancestor);
  // Folders about to arrive hold the lift, so sibling subtrees do not bob the stack.
  const lookaheadEnd = Math.min(index.rowCount - 1, readingRow + 1 + lookahead / rowHeight);
  for (let row = readingRow + 1; row <= lookaheadEnd; row++) {
    const arriving = expandedAt(row);
    if (arriving) candidates.set(arriving.displayIndex, arriving);
  }
  let shift = 0;
  // Departing branches return ancestors within the visible slots, independent of hidden depth.
  const scanEnd = Math.max(1, firstRow - 2 * cap);
  let returningDepth = reading.at(-1)?.entry.depth ?? 0;
  for (let row = readingRow; row >= scanEnd; row--) {
    const after = index.depthAt(row);
    const before = index.depthAt(row - 1);
    if (after < 0 || before < 0 || after > before) continue;
    const closing = expandedAt(row - 1);
    const deepest = closing ? before : before - 1;
    if (deepest < after) continue;
    const closedTop = row * rowHeight - scrollTop;
    const closingShift = demand(deepest, -Infinity, closedTop);
    const returningShift = (returningDepth - head) * rowHeight + closedTop - head * rowHeight;
    shift = Math.max(shift, closedTop > head * rowHeight ? closingShift : Math.min(closingShift, returningShift));
    if (closedTop <= head * rowHeight) continue;
    returningDepth = Math.max(returningDepth, deepest);
    for (const ancestor of index.chainAt(row - 1)) {
      if (ancestor.entry.depth >= after) candidates.set(ancestor.displayIndex, ancestor);
    }
    if (closing) candidates.set(closing.displayIndex, closing);
  }
  for (const ancestor of candidates.values()) {
    shift = Math.max(shift, demand(ancestor.entry.depth, rowTop(ancestor), endTop(ancestor)));
  }

  const slotTop = (depth: number) => depth < head ? depth * rowHeight : depth * rowHeight - shift;
  const ordered = [...candidates.values()]
    .sort((a, b) => a.entry.depth - b.entry.depth || a.displayIndex - b.displayIndex);
  const folders: StickyFolderSlot[] = [];
  const tops: number[] = [];
  const foldLine = new Map<StickyAncestor, boolean>();
  let above = -Infinity;
  // Pinned folders fold at the elision; scrolling folders fold beneath the head.
  for (const ancestor of ordered) {
    const depth = ancestor.entry.depth;
    const natural = rowTop(ancestor);
    if (natural >= slotTop(depth)) {
      foldLine.set(ancestor, natural < head * rowHeight);
      continue;
    }
    above = Math.max(above, Math.min(slotTop(depth), endTop(ancestor) - rowHeight));
    folders.push({ kind: "folder", key: ancestor.entry.key, ancestor, lifted: depth >= head });
    tops.push(above);
    foldLine.set(ancestor, above < (head + 1) * rowHeight);
  }
  if (shift <= 0 || cap < 2) return visible(folders, tops, rowHeight, shift, NO_STICKY_FOLD);

  const hidden = ordered.filter(ancestor => ancestor.entry.depth >= head && foldLine.get(ancestor) === true &&
    endTop(ancestor) - rowHeight >= slotTop(ancestor.entry.depth));
  const deepestHidden = hidden.at(-1)?.entry.depth ?? head;
  if (deepestHidden === head) return visible(folders, tops, rowHeight, shift, NO_STICKY_FOLD);

  const split = folders.findIndex(slot => slot.lifted);
  const headEnd = split < 0 ? folders.length : split;
  const headTop = headEnd > 0 ? tops[headEnd - 1]! : -rowHeight;
  const hiddenEnd = Math.max(...hidden.map(endTop));
  const elisionTop = Math.max(headTop, Math.min(head * rowHeight, hiddenEnd - rowHeight));
  const slots: StickySlot[] = folders.slice(0, headEnd);
  const slotTops = tops.slice(0, headEnd);
  slots.push({ kind: "elision", key: STICKY_ELISION_KEY, hidden });
  slotTops.push(elisionTop);
  // Partially covered folders keep painting while they slide under the elision.
  for (let i = headEnd; i < folders.length; i++) {
    if (tops[i]! <= elisionTop) continue;
    slots.push(folders[i]!);
    slotTops.push(tops[i]!);
  }
  return visible(slots, slotTops, rowHeight, shift, { floor: head, levels: deepestHidden - head });
}

/** Drops rows above the viewport or fully behind the row above them. */
function visible(
  slots: readonly StickySlot[],
  tops: readonly number[],
  rowHeight: number,
  shift: number,
  fold: StickyFold,
): StickyStack {
  const keptSlots: StickySlot[] = [];
  const keptTops: number[] = [];
  let height = 0;
  for (let i = 0; i < slots.length; i++) {
    const top = tops[i]!;
    const previous = keptTops.at(-1);
    if (top + rowHeight <= 0 || previous !== undefined && top <= previous) continue;
    keptSlots.push(slots[i]!);
    keptTops.push(top);
    height = Math.max(height, top + rowHeight);
  }
  if (keptSlots.length === 0 && fold.levels === 0) return EMPTY_STICKY_STACK;
  return { slots: keptSlots, tops: keptTops, height, shift, fold };
}
