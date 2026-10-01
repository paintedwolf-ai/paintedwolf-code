import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";

describe("parseAppState chat", () => {
  it("keeps a known message times choice", () => {
    for (const messageTimes of ["hover", "always"] as const) {
      expect(parseAppState({ version: 1, recents: [], chat: { messageTimes } }).chat).toEqual({
        messageTimes,
      });
    }
  });

  it("drops an unknown message times value and an empty slice", () => {
    expect(parseAppState({ version: 1, recents: [], chat: { messageTimes: "never" } }).chat).toBeUndefined();
    expect(parseAppState({ version: 1, recents: [], chat: {} }).chat).toBeUndefined();
  });
});
