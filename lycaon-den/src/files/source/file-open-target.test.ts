import { describe, expect, it } from "vitest";
import { fileOpenTarget } from "./file-open-target.ts";
const roots = [{ id: "main", path: "/repo" }, { id: "other", path: "/other" }];
const file = { kind: "text" as const, rootId: "other", path: "src/a.ts", sourcePresent: true };
describe("file destination targets", () => {
  it("uses the buffer's resolved root and preserves its line", () => {
    expect(fileOpenTarget(file, roots, 28)).toMatchObject({ absolutePath: "/other/src/a.ts", line: 28, unavailable: undefined });
  });
  it("does not reinterpret worker files or missing roots as primary checkout files", () => {
    expect(fileOpenTarget({ ...file, jobId: "worker-1" }, roots)?.unavailable).toContain("worker workspace");
    expect(fileOpenTarget({ ...file, rootId: "missing" }, roots)?.unavailable).toContain("unavailable");
  });
  it("disables absent files and omits composed documents with no disk target", () => {
    expect(fileOpenTarget({ ...file, sourcePresent: false }, roots)?.unavailable).toContain("not present on disk");
    expect(fileOpenTarget({ ...file, kind: "walk" }, roots)).toBeNull();
  });
});
