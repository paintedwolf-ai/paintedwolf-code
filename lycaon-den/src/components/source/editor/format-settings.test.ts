import { afterEach, describe, expect, it } from "vitest";
import type { FormatSettingsSource, FormatTool } from "../../../api/types.ts";
import { formattersForPath } from "./codemirror-lang.ts";
import {
  declaredLineWidth,
  editorConfigSettings,
  isFormatConfigPath,
  rememberFormatConfigNames,
  resetFormatConfigNamesForTests,
} from "./format-settings.ts";

const source = (tool: FormatTool, config: string, line_width?: number): FormatSettingsSource => ({
  tool, configs: [config], settings: line_width ? { line_width } : {},
});

describe("declared line width", () => {
  it("takes the nearest configured formatter of the language", () => {
    const sources = [
      source("editorconfig", ".editorconfig", 120),
      source("prettier", ".prettierrc", 100),
      source("biome", "web/biome.json", 90),
    ];
    expect(declaredLineWidth(sources, formattersForPath("web/app.ts"))).toBe(90);
    expect(declaredLineWidth(sources.slice(0, 2), formattersForPath("web/app.ts"))).toBe(100);
  });

  it("keeps the language's order between formatters at the same depth", () => {
    const sources = [source("black", "pyproject.toml", 100), source("ruff", "ruff.toml", 110)];
    expect(declaredLineWidth(sources, formattersForPath("tool.py"))).toBe(110);
  });

  it("ignores formatters of other languages and falls back to EditorConfig", () => {
    const sources = [source("editorconfig", ".editorconfig", 120), source("rustfmt", "rustfmt.toml", 100)];
    expect(declaredLineWidth(sources, formattersForPath("main.go"))).toBe(120);
    expect(declaredLineWidth([source("prettier", ".prettierrc")], formattersForPath("a.ts"))).toBeUndefined();
    expect(declaredLineWidth(undefined, formattersForPath("a.ts"))).toBeUndefined();
  });

  it("reads EditorConfig's own entry", () => {
    expect(editorConfigSettings([source("rustfmt", "rustfmt.toml", 100), source("editorconfig", ".editorconfig", 99)]))
      .toEqual({ line_width: 99 });
  });
});

describe("format configuration paths", () => {
  afterEach(resetFormatConfigNamesForTests);

  it("watches EditorConfig before the host names its files, then every name it reads", () => {
    expect(isFormatConfigPath("p", "pkg/.editorconfig")).toBe(true);
    expect(isFormatConfigPath("p", "pkg/.prettierrc")).toBe(false);
    rememberFormatConfigNames("p", [".editorconfig", ".prettierrc", "pyproject.toml"]);
    expect(isFormatConfigPath("p", "pkg/.prettierrc")).toBe(true);
    expect(isFormatConfigPath("p", "pyproject.toml")).toBe(true);
    expect(isFormatConfigPath("p", "pkg/prettierrc")).toBe(false);
    expect(isFormatConfigPath("other", "pkg/.prettierrc")).toBe(false);
    expect(isFormatConfigPath("p", undefined)).toBe(false);
  });
});
