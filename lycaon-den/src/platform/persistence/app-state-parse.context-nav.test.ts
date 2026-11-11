import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";

describe("parseAppState contextNav", () => {
  it("migrates a legacy visible Context pin list once by pinning running", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      contextNav: { visible: ["search", "artifacts"] },
    });
    expect(state.contextNav?.visible).toEqual([
      "search",
      "artifacts",
      "running",
    ]);
    expect(state.contextNav?.runningPinnedMigrated).toBe(true);
  });

  it("preserves a visible Context pin list after migration when running is unpinned", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      contextNav: {
        visible: ["search", "artifacts"],
        runningPinnedMigrated: true,
      },
    });
    expect(state.contextNav?.visible).toEqual(["search", "artifacts"]);
    expect(state.contextNav?.runningPinnedMigrated).toBe(true);
  });

  it("drops unknown Context pin ids", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      contextNav: {
        visible: ["search", "nope", "blueprints"],
        runningPinnedMigrated: true,
      },
    });
    expect(state.contextNav?.visible).toEqual(["search", "blueprints"]);
  });

  it("preserves an intentional empty visible list", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      contextNav: { visible: [] },
    });
    expect(state.contextNav?.visible).toEqual([]);
  });

  it("defaults missing contextNav", () => {
    const state = parseAppState({ version: 1, recents: [] });
    expect(state.contextNav).toBeUndefined();
  });
});
