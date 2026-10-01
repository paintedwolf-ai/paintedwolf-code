import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
  loadSourceCorpus,
  loadTypeScriptCorpus,
  requireNonEmpty,
  type SourceFile,
} from "./source-corpus.ts";

const fixtureRoots: string[] = [];

afterEach(() => {
  for (const root of fixtureRoots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function fixture(): string {
  const root = mkdtempSync(join(tmpdir(), "den-source-corpus-"));
  fixtureRoots.push(root);
  mkdirSync(join(root, "nested"), { recursive: true });
  mkdirSync(join(root, "node_modules", "dependency"), { recursive: true });
  writeFileSync(join(root, "a.ts"), "export const a = true;\n");
  writeFileSync(join(root, "nested", "b.test.ts"), "export const b = true;\n");
  writeFileSync(join(root, "nested", "c.tsx"), "export const c = <div />;\n");
  writeFileSync(join(root, "node_modules", "dependency", "d.ts"), "export {};\n");
  return root;
}

describe("source corpus", () => {
  it("loads deterministic filtered snapshots and lookup views", () => {
    const root = fixture();
    const corpus = loadSourceCorpus(root, {
      extensions: ["ts", ".tsx"],
      excludeTests: true,
    });

    expect(corpus.files.map((file) => file.rel)).toEqual(["a.ts", "nested/c.tsx"]);
    expect(corpus.file("./nested/c.tsx")?.text).toContain("<div />");
    expect(corpus.under("nested").map((file) => file.rel)).toEqual(["nested/c.tsx"]);
  });

  it("parses TypeScript once for normalized equivalent requests", () => {
    const root = fixture();
    const first = loadTypeScriptCorpus(root, { excludeTests: true });
    const second = loadTypeScriptCorpus(root, { excludeTests: true });

    expect(second).toBe(first);
    expect(first.files.map((file) => file.ast.fileName)).toEqual(
      first.files.map((file) => file.path),
    );
    expect(first.file("nested/c.tsx")?.ast.fileName).toBe(
      join(root, "nested", "c.tsx"),
    );
  });

  it("coalesces equivalent filter order and duplicates", () => {
    const root = fixture();
    const first = loadSourceCorpus(root, {
      extensions: ["ts", ".tsx", "ts"],
      skipDirectories: ["vendor", "node_modules", "vendor"],
    });
    const second = loadSourceCorpus(root, {
      extensions: [".tsx", ".ts"],
      skipDirectories: ["node_modules", "vendor"],
    });

    expect(second).toBe(first);
  });

  it("uses locale-independent lexical order", () => {
    const root = fixture();
    writeFileSync(join(root, "Z.ts"), "export const upper = true;\n");

    const corpus = loadSourceCorpus(root, { extensions: [".ts"] });

    expect(corpus.files.map((file) => file.rel)).toEqual([
      "Z.ts",
      "a.ts",
      "nested/b.test.ts",
    ]);
  });

  it("keeps a stable snapshot after files change", () => {
    const root = fixture();
    const corpus = loadSourceCorpus(root, { extensions: [".ts"] });
    writeFileSync(join(root, "a.ts"), "export const changed = true;\n");
    writeFileSync(join(root, "later.ts"), "export const later = true;\n");

    expect(corpus.file("a.ts")?.text).toBe("export const a = true;\n");
    expect(corpus.file("later.ts")).toBeUndefined();
  });

  it("cannot be mutated through a returned collection", () => {
    const corpus = loadSourceCorpus(fixture(), { extensions: [".ts"] });
    expect(() => {
      (corpus.files as SourceFile[]).push({ path: "bad", rel: "bad", text: "bad" });
    }).toThrow();
    expect(corpus.file("bad")).toBeUndefined();
  });

  it("lets an explicit skip set replace the defaults", () => {
    const root = fixture();
    mkdirSync(join(root, "custom"), { recursive: true });
    writeFileSync(join(root, "custom", "hidden.ts"), "export {};\n");
    const corpus = loadSourceCorpus(root, {
      extensions: [".ts"],
      skipDirectories: ["custom"],
    });

    expect(corpus.file("custom/hidden.ts")).toBeUndefined();
    expect(corpus.file("node_modules/dependency/d.ts")).toBeDefined();
  });

  it("rejects vacuous selections", () => {
    expect(() => requireNonEmpty("production widgets", [])).toThrow(
      "production widgets: source corpus selection is empty",
    );
  });
});
