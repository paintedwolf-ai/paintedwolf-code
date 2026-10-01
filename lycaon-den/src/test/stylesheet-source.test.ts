import { afterEach, describe, expect, it } from "vitest";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { readSourceText, stylesheetDependencies } from "./stylesheet-source.ts";

const roots: string[] = [];
function fixture(files: Record<string, string>): string {
  const root = mkdtempSync(join(tmpdir(), "stylesheet-source-"));
  roots.push(root);
  for (const [path, body] of Object.entries(files)) {
    mkdirSync(dirname(join(root, path)), { recursive: true });
    writeFileSync(join(root, path), body);
  }
  return root;
}
afterEach(() => { for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true }); });

describe("stylesheet source expansion", () => {
  it("preserves nested cascade order and ignores imports inside comments", () => {
    const root = fixture({
      "main.css": '/*\n@import "./absent.css";\n*/\n@import "./first.css";\n@import url("./nested/second.css");.last { color: green; }\n',
      "first.css": ".first { color: red; }\n",
      "nested/second.css": '  @import "../third.css";\n.second { color: blue; }\n',
      "third.css": ".third { color: white; }\n",
    });
    expect(readSourceText(join(root, "main.css"))).toBe('/*\n@import "./absent.css";\n*/\n.first { color: red; }\n  .third { color: white; }\n.second { color: blue; }\n.last { color: green; }\n');
    expect([...stylesheetDependencies(join(root, "main.css"))].map((path) => path.slice(root.length + 1)))
      .toEqual(["main.css", "first.css", "nested/second.css", "third.css"]);
  });

  it("retains package and conditional imports while discovering local dependencies", () => {
    const root = fixture({
      "main.css": '@import "package/styles.css";\n@import "./layer.css" layer(components);\n@import url(./print.css) print;\n',
      "layer.css": ".layer {}\n",
      "print.css": ".print {}\n",
    });
    expect(readSourceText(join(root, "main.css"))).toBe('@import "package/styles.css";\n@import "./layer.css" layer(components);\n@import url(./print.css) print;\n');
    expect(stylesheetDependencies(join(root, "main.css")).size).toBe(3);
  });

  it("deduplicates physical dependencies while retaining repeated cascade occurrences", () => {
    const root = fixture({ "main.css": '@import "./leaf.css";\n@import "./leaf.css";\n', "leaf.css": ".leaf {}\n" });
    expect(readSourceText(join(root, "main.css"))).toBe(".leaf {}\n.leaf {}\n");
    expect(stylesheetDependencies(join(root, "main.css")).size).toBe(2);
  });

  it("rejects cycles, missing imports, and malformed CSS", () => {
    const root = fixture({ "a.css": '@import "./b.css";', "b.css": '@import "./a.css";', "missing.css": '@import "./absent.css";', "broken.css": ".broken {" });
    expect(() => readSourceText(join(root, "a.css"))).toThrow("Cyclic stylesheet import");
    expect(() => stylesheetDependencies(join(root, "a.css"))).toThrow("Cyclic stylesheet import");
    expect(() => readSourceText(join(root, "missing.css"))).toThrow();
    expect(() => readSourceText(join(root, "broken.css"))).toThrow();
  });
});
