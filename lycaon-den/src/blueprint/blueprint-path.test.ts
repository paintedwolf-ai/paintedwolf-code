import { describe, expect, it } from "vitest";
import { boundBlueprintRelPath } from "./blueprint-path.ts";
import { overlayRel } from "../platform/files/overlay-dir.ts";

describe("boundBlueprintRelPath", () => {
  it("returns bound convention paths", () => {
    const bound = overlayRel("blueprints", "weather.md");
    expect(boundBlueprintRelPath(bound)).toBe(bound);
  });

  it("returns empty when unbound", () => {
    expect(boundBlueprintRelPath("")).toBe("");
    expect(boundBlueprintRelPath(null)).toBe("");
    expect(boundBlueprintRelPath("plans/weather.md")).toBe("");
  });

  it("binds blueprints only inside the overlay directory", () => {
    const path = ".paintedwolf/blueprints/weather.md";
    expect(boundBlueprintRelPath(path)).toBe(path);
    expect(boundBlueprintRelPath(".paintedwolf.bak/blueprints/weather.md")).toBe("");
  });
});
