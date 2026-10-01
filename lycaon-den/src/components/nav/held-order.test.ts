import { createRoot, createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import { createHeldOrder, type HeldEntry } from "./held-order.ts";

type Row = { title: string };

function entry(key: string, section = "chats", title = key): HeldEntry<Row> {
  return { key, section, item: { title } };
}

function harness(initial: HeldEntry<Row>[], inPlace: (key: string) => boolean = () => false) {
  return createRoot((dispose) => {
    const [source, setSource] = createSignal(initial);
    const [holding, setHolding] = createSignal(false);
    const order = createHeldOrder(source, holding, (e) => inPlace(e.key));
    const keys = () => order.presented().map((e) => e.key);
    return { order, setSource, setHolding, keys, dispose };
  });
}

describe("createHeldOrder", () => {
  it("follows the source while nothing holds it", () => {
    const h = harness([entry("a"), entry("b")]);
    h.setSource([entry("b"), entry("a")]);
    expect(h.keys()).toEqual(["b", "a"]);
    h.dispose();
  });

  it("keeps rows in place under the pointer and applies the order on release", () => {
    const h = harness([entry("a"), entry("b"), entry("c")]);
    h.setHolding(true);
    h.setSource([entry("c", "chats", "c replied"), entry("a"), entry("b")]);
    expect(h.keys()).toEqual(["a", "b", "c"]);
    expect(h.order.presented()[2]?.item.title).toBe("c replied");

    h.setHolding(false);
    expect(h.keys()).toEqual(["c", "a", "b"]);
    h.dispose();
  });

  it("adds new rows at the end and drops rows that left", () => {
    const h = harness([entry("a"), entry("b"), entry("c")]);
    h.setHolding(true);
    h.setSource([entry("new"), entry("a"), entry("b")]);
    expect(h.keys()).toEqual(["a", "b", "new"]);
    h.setSource([entry("new"), entry("b")]);
    expect(h.keys()).toEqual(["b", "new"]);
    h.dispose();
  });

  it("places the person's own new row where the source puts it", () => {
    const h = harness([entry("p", "pinned"), entry("a"), entry("b")], (key) => key === "mine");
    h.setHolding(true);
    h.setSource([entry("p", "pinned"), entry("mine"), entry("b"), entry("a")]);
    expect(h.keys()).toEqual(["p", "mine", "a", "b"]);
    h.dispose();
  });

  it("keeps a held row in its section even when the source moves it", () => {
    const h = harness([entry("p", "pinned"), entry("c")]);
    h.setHolding(true);
    h.setSource([entry("c"), entry("p", "chats")]);
    expect(h.order.presented().map((e) => `${e.section}:${e.key}`)).toEqual(["pinned:p", "chats:c"]);
    h.dispose();
  });

  it("presents the person's own change at once while held", () => {
    const h = harness([entry("a"), entry("b")]);
    h.setHolding(true);
    h.setSource([entry("b"), entry("a")]);
    expect(h.keys()).toEqual(["a", "b"]);
    h.order.rebase();
    expect(h.keys()).toEqual(["b", "a"]);
    h.setSource([entry("a"), entry("b")]);
    expect(h.keys()).toEqual(["b", "a"]);
    h.dispose();
  });
});
