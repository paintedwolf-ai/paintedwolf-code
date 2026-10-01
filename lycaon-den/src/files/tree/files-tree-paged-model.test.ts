import { expect, it } from "vitest";
import type { ProjectRoot, SourceTreeFrame, SourceTreeRow } from "../../api/types.ts";
import { UNKNOWN_SUBTREE_END, buildPagedTreeModel, treeFramesCover } from "./files-tree-paged-model.ts";

it("requires complete viewport coverage across adjacent and overlapping frames", () => {
  const spans = (...pairs: [number, number][]) => pairs.map(([start, end]) => ({ span: { start, end } }));
  expect(treeFramesCover(spans([200, 400], [0, 200]), 190, 212)).toBe(true);
  expect(treeFramesCover(spans([0, 200], [180, 380]), 190, 212)).toBe(true);
  expect(treeFramesCover(spans([0, 200], [201, 400]), 190, 212)).toBe(false);
  expect(treeFramesCover(spans([0, 200]), 190, 201)).toBe(false);
});
const roots: ProjectRoot[] = [{ id: "root", label: "repo", path: "/repo", kind: "attached", is_primary: true, added_at: "2026-09-15T00:00:00Z" }];
const row = (path: string, depth: number, kind: SourceTreeRow["kind"] = "directory"): SourceTreeRow => ({
  address: { root_id: "root", path }, name: path.split("/").at(-1)!, depth, kind, expanded: kind === "directory",
});
function frame(start: number, rows: SourceTreeRow[]): SourceTreeFrame {
  return { kind: "tree", view_id: "view", intent_revision: "intent", projection_revision: "revision",
    extent: { rows: 1_000_000, complete: true }, span: { start, end: start + rows.length }, anchor: rows[0]!.address,
    rows, ancestors: [{ index: 0, end: 1_000_000, row: row(".", 0) }, { index: 123, end: 900_000, row: row("src", 1) }] };
}
it("addresses distant rows and sticky ancestors without allocating the tree", () => {
  const model = buildPagedTreeModel(roots, 1_000_000, [frame(800_000, [row("src/a.ts", 2, "file"), row("src/b.ts", 2, "file")])]);
  expect(model.length).toBe(1_000_000);
  expect(model.indexOfEntry("root", "src/a.ts")).toBe(800_000);
  expect(model.chainAt(800_000).map(item => item.displayIndex)).toEqual([0, 123]);
  expect(model.nextOutsideAt(123)).toBe(900_000);
  expect(model.rowAt(42)).toBeUndefined();
  expect(model.nextEntryIndex(800_001, 1)).toBe(800_002);
});
it("keeps a host subtree end over a retained frame's unknown end in either frame order", () => {
  const retained: SourceTreeFrame = { ...frame(200, [row("src/old.ts", 2, "file")]),
    ancestors: [{ index: 0, end: UNKNOWN_SUBTREE_END, row: row(".", 0) }, { index: 123, end: UNKNOWN_SUBTREE_END, row: row("src", 1) }] };
  const fresh = frame(800_000, [row("src/a.ts", 2, "file")]);
  for (const frames of [[retained, fresh], [fresh, retained]]) {
    const model = buildPagedTreeModel(roots, 1_000_000, frames);
    expect(model.nextOutsideAt(123)).toBe(900_000);
  }
  // Alone, an unknown end leaves the folder open rather than closing it early.
  const open = buildPagedTreeModel(roots, 1_000_000, [retained]);
  expect(open.nextOutsideAt(123)).toBe(-1);
  expect(open.chainAt(200).map(item => item.displayIndex)).toEqual([0, 123]);
});
it("maps sparse draft rows between host and display ranks", () => {
  const model = buildPagedTreeModel(roots, 1_000_000, [frame(800_000, [row("src/a.ts", 2, "file")])],
    [{ rootId: "root", dir: "src", naming: "file", createError: "Already exists" }]);
  expect(model.rowAt(124)?.kind).toBe("naming");
  expect(model.rowAt(125)?.kind).toBe("create-error");
  expect(model.indexOfEntry("root", "src/a.ts")).toBe(800_002);
  expect(model.sourceIndex(800_002)).toBe(800_000);
  expect(model.sourceIndex(124)).toBe(123);
  expect(model.displayIndex(900_000)).toBe(900_002);
  expect(model.nextOutsideAt(123)).toBe(900_002);
});
it("does not retain an evicted frame in the next model", () => {
  const first = frame(800_000, [row("src/a.ts", 2, "file")]);
  const next = frame(900_001, [row("z.ts", 1, "file")]); next.ancestors = next.ancestors.slice(0, 1);
  const model = buildPagedTreeModel(roots, 1_000_000, [next]);
  expect(buildPagedTreeModel(roots, 1_000_000, [first]).indexOfEntry("root", "src/a.ts")).toBe(800_000);
  expect(model.indexOfEntry("root", "src/a.ts")).toBe(-1);
  expect(model.chainAt(900_001).map(item => item.entry.path)).toEqual(["."]);
});

it("does not invent a subtree end across missing frames", () => {
  const first = frame(10, [row("src/a", 2), row("src/a/first.ts", 3, "file")]);
  const last = frame(100, [row("src/z.ts", 2, "file")]);
  const model = buildPagedTreeModel(roots, 1_000_000, [first, last]);
  expect(model.nextOutsideAt(10)).toBe(-1);
  const contiguous = buildPagedTreeModel(roots, 1_000_000, [frame(10, [row("src/a", 2), row("src/a/first.ts", 3, "file"), row("src/b.ts", 2, "file")])]);
  expect(contiguous.nextOutsideAt(10)).toBe(12);
});

it("uses only the published extent for scroll geometry", () => {
  const model = buildPagedTreeModel(roots, 1_000, [frame(0, [row(".", 0), row("src", 1)])]);
  expect(model.length).toBe(1_000);
  expect(model.rowAt(1_000)).toBeUndefined();
  expect(model.lastEntryIndex()).toBe(999);
});

it("keeps known sticky ancestors through a page gap and honors subtree boundaries", () => {
  const model = buildPagedTreeModel(roots, 1_000_000, [frame(800_000, [row("src/a.ts", 2, "file")])]);
  expect(model.chainAt(800_100).map(item => item.entry.path)).toEqual([".", "src"]);
  expect(model.chainAt(900_000).map(item => item.entry.path)).toEqual(["."]);
  expect(model.chainAt(50).map(item => item.entry.path)).toEqual(["."]);
});
