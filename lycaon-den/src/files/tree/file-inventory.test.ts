import { describe, expect, it, beforeEach } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import type { SourceSearchMatch } from "../../api/types.ts";
import {
  filterSymbolsByQuery,
  orderIdleInventory,
  resetFileInventoryForTests,
  watchIndexedFiles,
  type InventoryFile,
} from "./file-inventory.ts";

function file(path: string, rootId = "r1"): InventoryFile {
  const slash = path.lastIndexOf("/");
  return {
    rootId,
    path,
    basename: slash < 0 ? path : path.slice(slash + 1),
    dir: slash < 0 ? "" : path.slice(0, slash),
    matchIndexes: [],
  };
}

async function published(matches: SourceSearchMatch[]): Promise<readonly InventoryFile[]> {
  let files: readonly InventoryFile[] = [];
  const client = stubClient({
    searchProjectSource: async () => ({ state: "ready", revision: 1, refreshing: false, coverage: [], matches }),
  });
  await watchIndexedFiles(client, "p", "q", (page) => { files = page; });
  return files;
}

describe("file-inventory", () => {
  beforeEach(() => {
    resetFileInventoryForTests();
  });

  const inventory = [file("src/mod.ts"), file("tools/demo.ts"), file("mod")];

  it("orders the idle list by recent opens, then open buffers, then the index", () => {
    const ordered = orderIdleInventory(inventory, {
      recent: [file("tools/demo.ts")],
      open: [file("mod"), file("tools/demo.ts")],
    }).map((e) => e.path);
    expect(ordered).toEqual(["tools/demo.ts", "mod", "src/mod.ts"]);
  });

  it("keeps equal paths in separate roots and applies recency by full address", () => {
    const left = file("src/main.go", "left");
    const right = file("src/main.go", "right");
    const ordered = orderIdleInventory([left, right], { recent: [right] });
    expect(ordered.map((entry) => entry.rootId)).toEqual(["right", "left"]);
  });

  it("keeps the host's rank order and maps code point highlights to UTF-16 indexes", async () => {
    const files = await published([
      { root_id: "r", path: "𝒳/Model.ts", highlights: [{ start: 0, end: 1 }, { start: 2, end: 5 }] },
      { root_id: "r", path: "a/b.ts", highlights: [] },
    ]);
    expect(files.map((entry) => entry.path)).toEqual(["𝒳/Model.ts", "a/b.ts"]);
    const [first, second] = files;
    expect(first!.matchIndexes).toEqual([0, 1, 3, 4, 5]);
    expect(first!.matchIndexes.map((index) => first!.path[index]).join("")).toBe("𝒳Mod");
    expect(first!.basename).toBe("Model.ts");
    expect(second!.matchIndexes).toEqual([]);
  });

  it("filterSymbolsByQuery keeps subsequence hits", () => {
    const syms = [
      { name: "Hello", line: 1 },
      { name: "Size", line: 2 },
      { name: "Box", line: 3 },
    ];
    expect(filterSymbolsByQuery(syms, "hl").map((s) => s.name)).toEqual([
      "Hello",
    ]);
  });
});
