import { describe, expect, it } from "vitest";
import { createNoticeStore } from "./notice-store.ts";
import { projectScope, sessionScope } from "./notice-scope.ts";
import { countForSession, countLabel } from "./notice-rollup.ts";

describe("countLabel", () => {
  it("is exact while it is worth reading, then clamps", () => {
    expect(countLabel(0)).toBe("");
    expect(countLabel(1)).toBe("1");
    expect(countLabel(9)).toBe("9");
    // Past this the exact number stops informing the decision to go look.
    expect(countLabel(10)).toBe("9+");
    expect(countLabel(250)).toBe("9+");
  });
});

describe("countForSession", () => {
  it("counts only that chat's own notices", () => {
    const store = createNoticeStore();
    store.publish({ message: "a" }, sessionScope("p1", "s1"));
    store.publish({ message: "b", code: "other" }, sessionScope("p1", "s1"));
    store.publish({ message: "c" }, sessionScope("p1", "s2"));
    // A project condition renders in this chat's dock but is not the chat's own.
    store.publish({ message: "d" }, projectScope("p1"));

    expect(countForSession(store.index(), "s1")).toBe(2);
    expect(countForSession(store.index(), "s2")).toBe(1);
  });
});
