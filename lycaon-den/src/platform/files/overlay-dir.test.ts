import { afterEach, expect, it, vi } from "vitest";

afterEach(() => {
  vi.unstubAllEnvs();
  vi.resetModules();
});

it.each([false, true])("uses shared project paths with DEV=%s", async (dev) => {
  vi.stubEnv("DEV", dev);
  vi.resetModules();
  const { overlayRel } = await import("./overlay-dir.ts");
  const { boundBlueprintRelPath } = await import("../../blueprint/blueprint-path.ts");
  const path = ".paintedwolf/blueprints/plan.md";
  expect(overlayRel(" /blueprints/ ", "plan.md")).toBe(path);
  expect(boundBlueprintRelPath(path)).toBe(path);
  expect(boundBlueprintRelPath(".paintedwolf.bak/blueprints/plan.md")).toBe("");
});
