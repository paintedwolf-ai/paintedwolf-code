import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";

describe("parseAppState chatList", () => {
  it.each(["created", "activity", "title"] as const)("keeps the %s sort", (sort) => {
    const state = parseAppState({ version: 1, recents: [], chatList: { sort } });
    expect(state.chatList).toEqual({ sort });
  });

  it("drops an unknown sort, leaving the default to the reader", () => {
    const state = parseAppState({ version: 1, recents: [], chatList: { sort: "updated" } });
    expect(state.chatList).toBeUndefined();
  });

  it("drops a malformed slice", () => {
    const state = parseAppState({ version: 1, recents: [], chatList: ["title"] });
    expect(state.chatList).toBeUndefined();
  });
});
