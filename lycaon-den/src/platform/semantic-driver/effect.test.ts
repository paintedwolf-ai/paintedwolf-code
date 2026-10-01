// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { describeElement } from "./core.ts";
import { beginEffect, endEffect, logRequest, noteAimedElement } from "./effect.ts";
import { readSnapshot } from "./page-api.ts";

function markVisible(el: HTMLElement): void {
  Object.defineProperty(el, "offsetParent", { configurable: true, get: () => document.body });
}

function square(index: number): HTMLElement {
  const el = document.createElement("div");
  el.className = "square dark piece-w";
  el.dataset.index = String(index);
  markVisible(el);
  document.body.appendChild(el);
  return el;
}

describe("action effect", () => {
  afterEach(() => {
    document.body.replaceChildren();
  });

  it("reports the class change a click handler made and the target afterwards", async () => {
    const el = square(12);
    el.addEventListener("click", () => el.classList.add("selected"));
    const { id } = beginEffect() as { id: number };
    noteAimedElement(el);
    el.click();
    const effect = await endEffect({ id });

    expect(effect.changed).toEqual([
      {
        element: expect.objectContaining({ tag: "div", className: "square dark piece-w selected" }),
        attribute: "class",
        before: "square dark piece-w",
        after: "square dark piece-w selected",
      },
    ]);
    expect(effect.target_after).toEqual(expect.objectContaining({ className: "square dark piece-w selected" }));
    expect(effect.requests).toBeUndefined();
  });

  it("reports an input the page ignored as no changes", async () => {
    const el = square(28);
    const { id } = beginEffect() as { id: number };
    noteAimedElement(el);
    el.click();
    const effect = await endEffect({ id });

    expect(effect.dom_changes).toBe(0);
    expect(effect.changed).toBeUndefined();
  });

  it("omits an attribute that changed and changed back", async () => {
    const el = square(12);
    const { id } = beginEffect() as { id: number };
    el.classList.add("selected");
    el.classList.remove("selected");
    const effect = await endEffect({ id });

    expect(effect.dom_changes).toBe(2);
    expect(effect.changed).toBeUndefined();
  });

  it("lists requests the step started with their outcome", async () => {
    const { id } = beginEffect() as { id: number };
    const entry = logRequest("post", "/api/move");
    entry.status = 200;
    const effect = await endEffect({ id });

    expect(effect.requests).toEqual([expect.objectContaining({ method: "POST", url: "/api/move", status: 200 })]);
  });

  it("counts nodes the step added and removed", async () => {
    const old = square(1);
    const { id } = beginEffect() as { id: number };
    old.remove();
    square(2);
    const effect = await endEffect({ id });

    expect(effect.nodes_added).toBe(1);
    expect(effect.nodes_removed).toBe(1);
  });

  it("says the document was replaced when its recording is gone", async () => {
    const effect = await endEffect({ id: -1 });
    expect(effect.document_replaced).toBe(true);
  });
});

describe("element brief", () => {
  afterEach(() => {
    document.body.replaceChildren();
  });

  it("keeps the whole class list and declared state", () => {
    const tab = document.createElement("button");
    tab.className = "tab tab-primary is-active";
    tab.setAttribute("aria-selected", "true");
    document.body.appendChild(tab);

    expect(describeElement(tab)).toEqual(
      expect.objectContaining({ className: "tab tab-primary is-active", state: { "aria-selected": "true" } }),
    );
  });
});

describe("snapshot bounds", () => {
  afterEach(() => {
    document.body.replaceChildren();
  });

  it("counts children past the per-node bound", () => {
    const board = document.createElement("div");
    markVisible(board);
    for (let i = 0; i < 64; i++) {
      const cell = document.createElement("div");
      markVisible(cell);
      board.appendChild(cell);
    }
    document.body.appendChild(board);

    const tree = readSnapshot().tree as { children?: { children_omitted?: number }[] };
    expect(tree.children?.[0]?.children_omitted).toBe(34);
  });
});
