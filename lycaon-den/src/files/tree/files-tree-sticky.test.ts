import { assert, describe, expect, it } from "vitest";
import {
  FILES_TREE_ROW_HEIGHT_PX,
  type FlatTreeRow,
} from "./project-files-tree-flat.ts";
import {
  STICKY_ELISION_KEY,
  retainStickySlots,
  stickyStackFromIndex,
  treeLayoutRange,
  type StickyAncestor,
  type StickySlot,
} from "./files-tree-sticky.ts";

import type { ProjectRoot, SourceTreeFrame, SourceTreeRow } from "../../api/types.ts";
import { buildPagedTreeModel } from "./files-tree-paged-model.ts";

type FixtureRow = { kind: "entry"; entry: FlatTreeRow } | { kind: "ancillary" };
const ROOT: ProjectRoot = {
  id: "r1", label: "repo", path: "/repo", kind: "attached", is_primary: true,
  added_at: "2026-09-15T00:00:00Z",
};
const H = FILES_TREE_ROW_HEIGHT_PX;
const EMPTY = { slots: [], tops: [], height: 0, shift: 0, fold: { floor: 0, levels: 0 } };
/** Tall enough that the item cap, not the viewport ratio, bounds the stack. */
const TALL = 2000;

function frameForRows(rows: FixtureRow[]): SourceTreeFrame {
  const source: SourceTreeRow[] = rows.map((row, index) => {
    if (row.kind === "entry") {
      const e = row.entry;
      return { address: { root_id: e.rootId, path: e.path }, name: e.name,
        depth: e.depth, kind: e.isDir ? "directory" : "file", expanded: e.expanded === true };
    }
    const previous = rows[index - 1];
    assert(previous?.kind === "entry", "A status row requires a parent directory.");
    return { address: { root_id: previous.entry.rootId, path: previous.entry.path },
      name: "", depth: previous.entry.depth + 1, kind: "empty", expanded: false };
  });
  return {
    kind: "tree", view_id: "view", intent_revision: "intent", projection_revision: "projection",
    extent: { rows: source.length, complete: true }, span: { start: 0, end: source.length },
    rows: source, ancestors: [], anchor: { root_id: "r1", path: "." },
  };
}

function pagedModel(rows: FixtureRow[]) {
  return buildPagedTreeModel([ROOT], rows.length, [frameForRows(rows)]);
}

function dir(
  path: string,
  name: string,
  depth: number,
  opts?: { isRoot?: boolean },
): FlatTreeRow {
  return {
    key: `r1\0${path}`,
    rootId: "r1",
    rootLabel: "repo",
    path,
    name,
    depth,
    isDir: true,
    isRoot: opts?.isRoot ?? path === ".",
    expanded: true,
  };
}

function file(path: string, name: string, depth: number): FlatTreeRow {
  return {
    key: `r1\0${path}`,
    rootId: "r1",
    rootLabel: "repo",
    path,
    name,
    depth,
    isDir: false,
    isRoot: false,
  };
}

function entry(e: FlatTreeRow): FixtureRow {
  return { kind: "entry", entry: e };
}

function nestedRows(): FixtureRow[] {
  return [
    entry(dir(".", "repo", 0, { isRoot: true })),
    entry(dir("src", "src", 1)),
    entry(dir("src/components", "components", 2)),
    entry(file("src/components/App.tsx", "App.tsx", 3)),
    entry(file("src/components/Button.tsx", "Button.tsx", 3)),
    entry(file("src/main.ts", "main.ts", 2)),
    entry(file("README.md", "README.md", 1)),
  ];
}

/** A root with `depth` directly nested folders, then `files` files in the innermost. */
function deepRows(depth: number, files: number): FixtureRow[] {
  const rows: FixtureRow[] = [entry(dir(".", "repo", 0, { isRoot: true }))];
  let path = "";
  for (let d = 1; d <= depth; d++) {
    path = path ? `${path}/d${d}` : `d${d}`;
    rows.push(entry(dir(path, `d${d}`, d)));
  }
  for (let i = 0; i < files; i++) rows.push(entry(file(`${path}/f${i}.ts`, `f${i}.ts`, depth + 1)));
  return rows;
}

/** Deep nesting with files between folders and subtrees ending at several levels. */
function branchingRows(): FixtureRow[] {
  const rows: FixtureRow[] = [entry(dir(".", "repo", 0, { isRoot: true }))];
  const nest = (parent: string, depth: number, remaining: number, label: string) => {
    const path = parent ? `${parent}/${label}${depth}` : `${label}${depth}`;
    rows.push(entry(dir(path, `${label}${depth}`, depth)));
    for (let i = 0; i < (depth % 3) + 1; i++) rows.push(entry(file(`${path}/a${i}.ts`, `a${i}.ts`, depth + 1)));
    if (remaining > 0) nest(path, depth + 1, remaining - 1, label);
    for (let i = 0; i < depth % 2 + 1; i++) rows.push(entry(file(`${path}/z${i}.ts`, `z${i}.ts`, depth + 1)));
  };
  nest("", 1, 11, "a");
  nest("", 1, 9, "b");
  rows.push(entry(file("README.md", "README.md", 1)));
  return rows;
}

function stack(rows: FixtureRow[], scrollTop: number, viewport: number, maxItems = 7) {
  return stickyStackFromIndex(pagedModel(rows), scrollTop, viewport, maxItems);
}

function paths(slots: readonly StickySlot[]): string[] {
  return slots.map(slot => slot.kind === "folder" ? slot.ancestor.entry.path : `⋯${slot.hidden.length}`);
}

function hiddenPaths(slots: readonly StickySlot[]): string[] {
  const elision = slots.find(slot => slot.kind === "elision");
  return elision?.kind === "elision" ? elision.hidden.map(ancestor => ancestor.entry.path) : [];
}

describe("sticky index ancestry", () => {
  it("returns root-first directory ancestors excluding the anchor row", () => {
    const ancestors = pagedModel(nestedRows()).chainAt(3);
    expect(ancestors.map((a) => a.entry.path)).toEqual([".", "src", "src/components"]);
    expect(ancestors.map((a) => a.displayIndex)).toEqual([0, 1, 2]);
  });

  it("keeps the loading directory in its status row ancestry", () => {
    const rows: FixtureRow[] = [
      entry(dir(".", "repo", 0, { isRoot: true })),
      entry(dir("src", "src", 1)),
      { kind: "ancillary" },
    ];
    expect(pagedModel(rows).chainAt(2).map((a) => a.entry.path)).toEqual([".", "src"]);
  });
});

describe("sticky stack", () => {
  it.each([3, 4, 5, 6, 7, 8, 9, 10])("keeps the root and inner folders within %i rows", maxItems => {
    const fits = stack(deepRows(maxItems - 1, 40), 25 * H, 1000, maxItems);
    expect(fits.slots).toHaveLength(maxItems);
    expect(fits.fold).toEqual({ floor: 0, levels: 0 });
    const folded = stack(deepRows(12, 40), 30 * H, 1000, maxItems);
    expect(folded.slots).toHaveLength(maxItems);
    expect(paths(folded.slots)[0]).toBe(".");
    expect(folded.slots[1]?.kind).toBe("elision");
    expect(folded.slots.slice(2).map(slot => slot.kind === "folder" ? slot.ancestor.entry.depth : -1))
      .toEqual(Array.from({ length: maxItems - 2 }, (_, i) => 15 - maxItems + i));
    expect(folded.height).toBe(maxItems * H);
    expect(folded.fold).toEqual({ floor: 1, levels: 13 - maxItems });
  });

  it("disables both sticky rows and indentation folding", () => {
    const result = stack(deepRows(12, 40), 30 * H, 1000, 0);
    expect(result.slots).toEqual([]);
    expect(result.height).toBe(0);
    expect(result.shift).toBe(0);
    expect(result.fold).toEqual({ floor: 0, levels: 0 });
  });

  const viewport = 320;

  it("is empty at scrollTop 0", () => {
    expect(stack(nestedRows(), 0, viewport)).toEqual(EMPTY);
  });

  it("pins ancestors once nested content scrolls under the top", () => {
    const rows: FixtureRow[] = [
      entry(dir(".", "repo", 0, { isRoot: true })),
      entry(dir("src", "src", 1)),
      entry(dir("src/components", "components", 2)),
      ...Array.from({ length: 8 }, (_, i) => entry(file(`src/components/f${i}.ts`, `f${i}.ts`, 3))),
      entry(file("src/main.ts", "main.ts", 2)),
    ];
    const sticky = stack(rows, 3 * H, viewport);
    expect(paths(sticky.slots)).toEqual([".", "src", "src/components"]);
    expect(sticky.tops).toEqual([0, H, 2 * H]);
    expect(sticky.height).toBe(3 * H);
  });

  it("pins a folder while its own row is still on screen", () => {
    const sticky = stack(nestedRows(), 1, viewport);
    expect(paths(sticky.slots)).toEqual([".", "src", "src/components"]);
    expect(sticky.tops).toEqual([0, H, 2 * H]);
  });

  it("slides the innermost folder up behind the one above it", () => {
    const sticky = stack(nestedRows(), 5 * H - 3 * H + 10, viewport);
    expect(paths(sticky.slots)).toEqual([".", "src", "src/components"]);
    expect(sticky.tops).toEqual([0, H, 2 * H - 10]);
    expect(sticky.height).toBe(3 * H - 10);
  });

  it("keeps a collapsed folder out of the sticky band", () => {
    const collapsed = dir("src", "src", 1);
    collapsed.expanded = false;
    const rows = [entry(dir(".", "repo", 0)), entry(collapsed),
      ...Array.from({ length: 12 }, (_, i) => entry(file(`f${i}.ts`, `f${i}.ts`, 1)))];
    expect(paths(stack(rows, 3 * H, viewport).slots)).toEqual(["."]);
  });

  it("keeps an unchanged stack when a chain fits under the cap", () => {
    const sticky = stack(deepRows(6, 30), 20 * H, TALL);
    expect(paths(sticky.slots)).toEqual([".", "d1", "d1/d2", "d1/d2/d3", "d1/d2/d3/d4", "d1/d2/d3/d4/d5", "d1/d2/d3/d4/d5/d6"]);
    expect(sticky.tops).toEqual([0, 1, 2, 3, 4, 5, 6].map(slot => slot * H));
  });

  it("pins the root, folds the middle, and keeps the innermost folders past the cap", () => {
    const sticky = stack(deepRows(12, 40), 30 * H, TALL);
    expect(paths(sticky.slots)).toEqual([".", "⋯7",
      "d1/d2/d3/d4/d5/d6/d7/d8",
      "d1/d2/d3/d4/d5/d6/d7/d8/d9",
      "d1/d2/d3/d4/d5/d6/d7/d8/d9/d10",
      "d1/d2/d3/d4/d5/d6/d7/d8/d9/d10/d11",
      "d1/d2/d3/d4/d5/d6/d7/d8/d9/d10/d11/d12"]);
    expect(hiddenPaths(sticky.slots)).toEqual(["d1", "d1/d2", "d1/d2/d3", "d1/d2/d3/d4", "d1/d2/d3/d4/d5",
      "d1/d2/d3/d4/d5/d6", "d1/d2/d3/d4/d5/d6/d7"]);
    expect(sticky.tops).toEqual([0, 1, 2, 3, 4, 5, 6].map(slot => slot * H));
    expect(sticky.height).toBe(7 * H);
    // d8 indents one level below the elision.
    expect(sticky.fold).toEqual({ floor: 1, levels: 6 });
    expect(sticky.shift).toBe(6 * H);
  });

  it("folds a contiguous run while its rows keep scrolling natively", () => {
    const rows = deepRows(13, 30);
    expect(stack(rows, H, TALL).slots.map(slot => slot.kind)).toEqual(["folder"]);
    const sticky = stack(rows, H + 1, TALL);
    expect(paths(sticky.slots)).toEqual([".", "⋯2"]);
    expect(sticky.tops).toEqual([0, H]);
    expect(sticky.fold).toEqual({ floor: 1, levels: 1 });
  });

  it("does not fold while the stack fits", () => {
    expect(stack(deepRows(6, 30), 20 * H, TALL).fold).toEqual({ floor: 0, levels: 0 });
  });

  it("lifts the pinned folders under the elision while a deeper folder arrives", () => {
    const rows = deepRows(6, 20);
    const inner = "d1/d2/d3/d4/d5/d6/d7";
    rows.push(entry(dir(inner, "d7", 7)));
    for (let i = 0; i < 20; i++) rows.push(entry(file(`${inner}/g${i}.ts`, `g${i}.ts`, 8)));
    // The lift is 10px into its ramp while d7's row is still five rows below the last slot.
    const sticky = stack(rows, 15 * H + 10, TALL);
    expect(paths(sticky.slots)).toEqual([".", "⋯2", "d1/d2", "d1/d2/d3", "d1/d2/d3/d4", "d1/d2/d3/d4/d5", "d1/d2/d3/d4/d5/d6"]);
    expect(hiddenPaths(sticky.slots)).toEqual(["d1", "d1/d2"]);
    expect(sticky.tops).toEqual([0, H, 2 * H - 10, 3 * H - 10, 4 * H - 10, 5 * H - 10, 6 * H - 10]);
    expect(sticky.height).toBe(7 * H - 10);
    expect(sticky.fold).toEqual({ floor: 1, levels: 1 });
    expect(sticky.slots.map(slot => slot.kind === "folder" && slot.lifted)).toEqual([false, false, true, true, true, true, true]);
  });

  it("never grows past the cap", () => {
    const rows = branchingRows();
    for (let scrollTop = 1; scrollTop <= (rows.length - 1) * H; scrollTop++) {
      const sticky = stack(rows, scrollTop, TALL);
      expect({ scrollTop, height: sticky.height <= 7 * H, slots: sticky.slots.length <= 8 })
        .toEqual({ scrollTop, height: true, slots: true });
    }
  });

  it("omits the root when the viewport fits only two rows", () => {
    const sticky = stack(deepRows(9, 40), 25 * H, 140);
    expect(paths(sticky.slots)).toEqual(["⋯9", "d1/d2/d3/d4/d5/d6/d7/d8/d9"]);
    expect(sticky.tops).toEqual([0, H]);
    expect(sticky.fold).toEqual({ floor: 0, levels: 8 });
  });

  it("pins only the innermost folder when the viewport fits one row", () => {
    const sticky = stack(deepRows(9, 40), 25 * H, 70);
    expect(paths(sticky.slots)).toEqual(["d1/d2/d3/d4/d5/d6/d7/d8/d9"]);
    expect(sticky.tops).toEqual([0]);
  });

  it("keeps the root and one inner folder at three rows", () => {
    const sticky = stack(deepRows(9, 40), 25 * H, 200);
    expect(paths(sticky.slots)).toEqual([".", "⋯8", "d1/d2/d3/d4/d5/d6/d7/d8/d9"]);
    expect(sticky.fold).toEqual({ floor: 1, levels: 7 });
  });

  it("caps by viewport ratio", () => {
    expect(stack(nestedRows(), 3 * H, 50)).toEqual(EMPTY);
  });
});

type Frame = {
  /** Painted tops by display index; pinned rows replace their natural rows. */
  tops: Map<number, number>;
  /** Pinned rows and the folder or elision painted above them. */
  slots: Map<number, { top: number; above: number | typeof STICKY_ELISION_KEY | undefined }>;
  hidden: Set<number>;
  elisionTop: number | undefined;
  height: number;
  pinned: Set<number>;
};

function frameAt(model: ReturnType<typeof pagedModel>, rowCount: number, scrollTop: number, viewport: number, maxItems = 7): Frame {
  const sticky = stickyStackFromIndex(model, scrollTop, viewport, maxItems);
  const tops = new Map<number, number>();
  // Natural rows paint only where the band leaves them uncovered.
  for (let i = 0; i < rowCount; i++) {
    const top = i * H - scrollTop;
    if (top + H > sticky.height) tops.set(i, top);
  }
  const slots: Frame["slots"] = new Map();
  let above: number | typeof STICKY_ELISION_KEY | undefined;
  const hidden = new Set<number>();
  const pinned = new Set<number>();
  let elisionTop: number | undefined;
  sticky.slots.forEach((slot, index) => {
    const top = sticky.tops[index]!;
    if (slot.kind === "elision") {
      elisionTop = top;
      above = STICKY_ELISION_KEY;
      for (const ancestor of slot.hidden) {
        hidden.add(ancestor.displayIndex);
        pinned.add(ancestor.displayIndex);
        tops.delete(ancestor.displayIndex);
      }
      return;
    }
    pinned.add(slot.ancestor.displayIndex);
    tops.set(slot.ancestor.displayIndex, top);
    slots.set(slot.ancestor.displayIndex, { top, above });
    above = slot.ancestor.displayIndex;
  });
  return { tops, slots, hidden, elisionTop, height: sticky.height, pinned };
}

function expectContinuousScroll(rows: FixtureRow[], viewport: number, maxItems = 7,
  range = { start: 0, end: (rows.length - 1) * H }) {
  const model = pagedModel(rows);
  let previous = frameAt(model, rows.length, range.start, viewport, maxItems);
  for (let scrollTop = range.start + 1; scrollTop <= range.end; scrollTop++) {
    const current = frameAt(model, rows.length, scrollTop, viewport, maxItems);
    const within = (label: string, index: number, from: number, to: number) => {
      const moved = Math.abs(to - from);
      if (!Number.isFinite(moved) || moved > 1) {
        assert.fail(JSON.stringify({ viewport, scrollTop, label, index, from, to, moved }));
      }
    };
    for (const [index, top] of current.tops) {
      const before = previous.tops.get(index);
      if (before !== undefined) within("row", index, before, top);
      else if (previous.hidden.has(index) && previous.elisionTop !== undefined) within("emerged", index, previous.elisionTop, top);
      else if (current.slots.has(index)) within("pinned", index, current.height, top + H);
      else within("uncovered", index, current.height, top + H);
    }
    // A pinned row may only vanish behind the row above it in this frame or into the elision.
    for (const [index, slot] of previous.slots) {
      if (current.tops.has(index)) continue;
      const cover = current.hidden.has(index) || slot.above === STICKY_ELISION_KEY
        ? current.elisionTop : current.tops.get(slot.above ?? -1);
      if (cover === undefined) continue;
      within("vanished", index, cover, Math.max(cover, slot.top));
    }
    if (previous.elisionTop !== undefined && current.elisionTop !== undefined) {
      within("elision", -1, previous.elisionTop, current.elisionTop);
    }
    previous = current;
  }
}

describe("sticky stack motion", () => {
  it.each([3, 5, 10])("rolls up a 128-level tree without a depth cutoff at %i visible levels", maxItems => {
    const rows = deepRows(128, 10);
    const end = rows.length;
    for (let i = 0; i < 160; i++) rows.push(entry(file(`root-${i}.ts`, `root-${i}.ts`, 1)));
    const model = pagedModel(rows);
    const pinned = stickyStackFromIndex(model, 128 * H, TALL, maxItems);
    expect(pinned.slots).toHaveLength(maxItems);
    expect(pinned.fold).toEqual({ floor: 1, levels: 129 - maxItems });
    expectContinuousScroll(rows, TALL, maxItems);
    for (let top = (end - maxItems) * H + 1; top < (end + 130) * H; top++) {
      const next = stickyStackFromIndex(model, top, TALL, maxItems);
      expect(next.height).toBeLessThanOrEqual(maxItems * H);
    }
    expect(stickyStackFromIndex(model, (end + maxItems) * H, TALL, maxItems).shift).toBe(0);
  });

  it.each([0, 3, 4, 5, 6, 7, 8, 9, 10])("matches warm and cold viewports after a deep branch at %i visible levels", maxItems => {
    for (const parentDepth of [0, 6]) {
      const rows = deepRows(128, 30);
      const end = rows.length;
      const parent = parentDepth ? Array.from({ length: parentDepth }, (_, index) => `d${index + 1}`).join("/") : "z";
      if (!parentDepth) rows.push(entry(dir(parent, parent, 1)));
      for (let i = 0; i < 180; i++) rows.push(entry(file(`${parent}/after-${i}.ts`, `after-${i}.ts`, parentDepth ? parentDepth + 1 : 2)));
      const frame = frameForRows(rows);
      const warm = buildPagedTreeModel([ROOT], rows.length, [frame]);
      for (let after = -maxItems; after < 150; after++) {
        const at = end + after;
        const { start, end: stop } = treeLayoutRange(at, at + Math.ceil(TALL / H), rows.length);
        const cold = buildPagedTreeModel([ROOT], rows.length, [{ ...frame, rows: frame.rows.slice(start, stop), span: { start, end: stop },
          ancestors: warm.chainAt(start).map(ancestor => ({ index: ancestor.displayIndex,
            end: warm.nextOutsideAt(ancestor.displayIndex) < 0 ? rows.length : warm.nextOutsideAt(ancestor.displayIndex),
            row: frame.rows[ancestor.displayIndex]!,
          })),
        }]);
        expect(stickyStackFromIndex(cold, at * H, TALL, maxItems))
          .toEqual(stickyStackFromIndex(warm, at * H, TALL, maxItems));
      }
      if (maxItems) expect(paths(stickyStackFromIndex(warm, (end + 100) * H, TALL, maxItems).slots)).toContain(parent);
      if (maxItems) expectContinuousScroll(rows, TALL, maxItems,
        { start: (end - maxItems) * H, end: (end + 2 * maxItems) * H });
    }
  });

  it.each([0, 3, 4, 5, 6, 7, 8, 9, 10])("preserves folding motion with a %i-row setting", maxItems => {
    for (const viewport of [140, 320, 1000]) expectContinuousScroll(branchingRows(), viewport, maxItems);
  });

  it.each([3, 4, 5, 6, 7, 8, 9, 10])("holds the lifted stack still across sibling subtrees with %i visible levels", maxItems => {
    for (const between of [0, 1, 3]) {
      const rows = deepRows(10, 0);
      const parent = "d1/d2/d3/d4/d5/d6/d7/d8/d9/d10";
      for (let i = 0; i < 30; i++) {
        rows.push(entry(dir(`${parent}/values-${i}`, `values-${i}`, 11)));
        rows.push(entry(file(`${parent}/values-${i}/strings.xml`, "strings.xml", 12)));
        for (let f = 0; f < between; f++) rows.push(entry(file(`${parent}/x${i}-${f}.xml`, `x${i}-${f}.xml`, 11)));
      }
      const model = pagedModel(rows);
      const shifts = new Set<number>();
      const parentTops = new Set<number>();
      for (let scrollTop = 20 * H; scrollTop < (rows.length - 20) * H; scrollTop++) {
        const sticky = stickyStackFromIndex(model, scrollTop, TALL, maxItems);
        shifts.add(sticky.shift);
        sticky.slots.forEach((slot, index) => {
          if (slot.kind === "folder" && slot.ancestor.entry.path === parent) parentTops.add(sticky.tops[index]!);
        });
      }
      expect({ between, shifts: [...shifts], parentTops: [...parentTops] })
        .toEqual({ between, shifts: [(12 - maxItems) * H], parentTops: maxItems === 3 ? [] : [(maxItems - 2) * H] });
    }
  });

  it("moves every row by at most one pixel per pixel of scroll", () => {
    const rows: FixtureRow[] = [
      entry(dir(".", "repo", 0, { isRoot: true })),
      entry(dir("src", "src", 1)),
      entry(dir("src/components", "components", 2)),
      ...Array.from({ length: 12 }, (_, i) => entry(file(`src/components/c${i}.tsx`, `c${i}.tsx`, 3))),
      entry(dir("src/lib", "lib", 2)),
      ...Array.from({ length: 12 }, (_, i) => entry(file(`src/lib/l${i}.ts`, `l${i}.ts`, 3))),
      entry(file("README.md", "README.md", 1)),
    ];
    expectContinuousScroll(rows, 320);
  });

  it("stays continuous while folders fold into and out of the elision", () => {
    expectContinuousScroll(deepRows(13, 30), TALL);
    expectContinuousScroll(branchingRows(), TALL);
  });

  it("stays continuous in short viewports", () => {
    for (const viewport of [140, 200, 320]) expectContinuousScroll(branchingRows(), viewport);
  });

  it("never covers a whole folder row without pinning or folding it", () => {
    for (const rows of [nestedRows(), deepRows(13, 30), branchingRows()]) {
      const model = pagedModel(rows);
      for (let scrollTop = 1; scrollTop <= (rows.length - 1) * H; scrollTop++) {
        const frame = frameAt(model, rows.length, scrollTop, TALL);
        for (let i = Math.floor(scrollTop / H); i < rows.length; i++) {
          const row = rows[i];
          if (i * H - scrollTop >= frame.height) break;
          if (i * H - scrollTop + H >= frame.height) continue;
          if (row?.kind !== "entry" || !row.entry.isDir) continue;
          // Folders leaving their subtree are outside the continuity window.
          const end = model.nextOutsideAt(i);
          if (end >= 0 && end * H - scrollTop <= frame.height) continue;
          expect({ scrollTop, covered: row.entry.path })
            .toEqual({ scrollTop, covered: frame.pinned.has(i) ? row.entry.path : "not pinned" });
        }
      }
    }
  });
});

describe("sticky slot retention", () => {
  const ancestor = (index: number, path: string, depth: number): StickyAncestor =>
    ({ displayIndex: index, entry: dir(path, path, depth) });

  it("retains unchanged folder slots when deeper context changes", () => {
    const root: StickySlot = { kind: "folder", key: "r1\0.", ancestor: ancestor(0, ".", 0), lifted: false };
    const parent: StickySlot = { kind: "folder", key: "r1\0src", ancestor: ancestor(1, "src", 1), lifted: true };
    const moved: StickySlot = { ...parent, ancestor: { ...parent.ancestor, displayIndex: 20 } };
    const copy: StickySlot = { ...root, ancestor: { ...root.ancestor, entry: { ...root.ancestor.entry } } };
    const next = retainStickySlots([root, parent], [copy, moved]);
    expect(next[0]).toBe(root);
    expect(next[1]).toBe(moved);
    expect(retainStickySlots(next, next.map(slot => ({ ...slot })))).toBe(next);
    const renamed: StickySlot = { ...root, ancestor: { ...root.ancestor, entry: { ...root.ancestor.entry, name: "new root" } } };
    expect(retainStickySlots(next, [renamed])[0]).toBe(renamed);
  });

  it("retains the elision until its hidden folders change", () => {
    const hidden = [ancestor(1, "a", 1), ancestor(2, "a/b", 2)];
    const elision: StickySlot = { kind: "elision", key: STICKY_ELISION_KEY, hidden };
    const same: StickySlot = { kind: "elision", key: STICKY_ELISION_KEY, hidden: hidden.map(item => ({ ...item })) };
    expect(retainStickySlots([elision], [same])[0]).toBe(elision);
    const grown: StickySlot = { kind: "elision", key: STICKY_ELISION_KEY, hidden: [...hidden, ancestor(3, "a/b/c", 3)] };
    expect(retainStickySlots([elision], [grown])[0]).toBe(grown);
  });
});
