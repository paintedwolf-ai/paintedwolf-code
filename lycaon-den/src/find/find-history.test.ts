// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import {
  closeFind,
  findHistoryDown,
  findHistoryUp,
  openFind,
  registerFindableView,
  resetFindControllerForTests,
  setFindQuery,
  setPrimaryFindableView,
} from "./find-controller.ts";

afterEach(() => {
  resetFindControllerForTests();
  document.body.replaceChildren();
});

describe("find query history", () => {
  function stub() {
    const root = document.createElement("div");
    root.textContent = "alpha";
    document.body.appendChild(root);
    registerFindableView({
      id: "stub",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("stub");
    openFind();
  }

  it("keeps the last 10 queries and ArrowUp walks them", () => {
    stub();
    for (let i = 0; i < 11; i++) {
      setFindQuery(`q${i}`);
      closeFind();
      openFind();
    }
    setFindQuery("");
    let current = "";
    for (let i = 10; i >= 1; i--) {
      const next = findHistoryUp(current);
      expect(next).toBe(`q${i}`);
      current = next!;
    }
    expect(findHistoryUp("q1")).toBe("q1");
    for (let i = 2; i <= 10; i++) {
      const next = findHistoryDown(current);
      expect(next).toBe(`q${i}`);
      current = next!;
    }
    expect(findHistoryDown("q10")).toBe("");
  });

  it("clears on controller reset (reload)", () => {
    stub();
    setFindQuery("kept");
    closeFind();
    expect(findHistoryUp("")).toBe("kept");
    resetFindControllerForTests();
    expect(findHistoryUp("")).toBeNull();
  });
});
