import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";

describe("parseAppState whatsNew", () => {
  it("preserves lastSeenVersion", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      whatsNew: { lastSeenVersion: "0.1.0" },
    });
    expect(state.whatsNew).toEqual({ lastSeenVersion: "0.1.0" });
  });

  it("drops a non-string lastSeenVersion", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      whatsNew: { lastSeenVersion: 12 },
    });
    expect(state.whatsNew).toBeUndefined();
  });
});
