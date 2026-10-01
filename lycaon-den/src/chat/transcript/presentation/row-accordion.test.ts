// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { createRowAccordion, type AccordionRow } from "./row-accordion.ts";

type FakeRow = AccordionRow & {
  open: boolean;
  applied: boolean[];
};

function fakeRow(key: string): FakeRow {
  const el = document.createElement("div");
  document.body.append(el);
  const row: FakeRow = {
    key,
    open: false,
    applied: [],
    el: () => el,
    domOpen: () => row.open,
    apply: (next) => {
      row.open = next;
      row.applied.push(next);
    },
  };
  return row;
}

describe("createRowAccordion", () => {
  it("closes a restored open row on the first click after a virtual remount", () => {
    const accordion = createRowAccordion();
    const first = fakeRow("a");
    const drop = accordion.register(first);
    accordion.toggle("a");
    drop();
    const restored = fakeRow("a");
    restored.open = true;
    accordion.register(restored);

    accordion.toggle("a");

    expect(restored.applied).toEqual([false]);
  });

  it("ignores an older registration's cleanup after replacement", () => {
    const accordion = createRowAccordion();
    const first = fakeRow("a");
    const drop = accordion.register(first);
    accordion.toggle("a");
    const replacement = fakeRow("a");
    replacement.open = true;
    accordion.register(replacement);
    drop();

    accordion.toggle("a");

    expect(replacement.applied).toEqual([false]);
  });

  it("reverses an in-flight close without adopting its lingering open DOM", () => {
    const accordion = createRowAccordion();
    const row = fakeRow("a");
    accordion.register(row);
    accordion.toggle("a");
    row.domOpen = () => true;
    accordion.toggle("a");
    accordion.toggle("a");
    expect(row.applied).toEqual([true, false, true]);
  });

  it("opens a row and closes the one before it", () => {
    const accordion = createRowAccordion();
    const a = fakeRow("a");
    const b = fakeRow("b");
    accordion.register(a);
    accordion.register(b);

    accordion.toggle("a");
    expect(a.open).toBe(true);

    accordion.toggle("b");
    expect(a.open).toBe(false);
    expect(b.open).toBe(true);
  });

  it("closes the open row when it is toggled again", () => {
    const accordion = createRowAccordion();
    const a = fakeRow("a");
    accordion.register(a);

    accordion.toggle("a");
    accordion.toggle("a");
    expect(a.open).toBe(false);
    expect(a.applied).toEqual([true, false]);
  });

  it("opens a row without closing it when it is already open", () => {
    const accordion = createRowAccordion();
    const a = fakeRow("a");
    accordion.register(a);

    accordion.open("a");
    accordion.open("a");
    expect(a.open).toBe(true);
    expect(a.applied).toEqual([true]);
  });

  it("leaves the closing row alone while it is still animating shut", () => {
    // Closing DOM state lags intent.
    const accordion = createRowAccordion();
    const a = fakeRow("a");
    const b = fakeRow("b");
    accordion.register(a);
    accordion.register(b);

    accordion.toggle("a");
    a.domOpen = () => true;
    accordion.toggle("a");
    expect(a.applied).toEqual([true, false]);
  });

  it("adopts a row that came back open from its own store on remount", () => {
    const accordion = createRowAccordion();
    const restored = fakeRow("restored");
    restored.open = true;
    const other = fakeRow("other");
    accordion.register(restored);
    accordion.register(other);

    // Restored state is discovered from mounted rows.
    accordion.toggle("other");
    expect(restored.open).toBe(false);
    expect(other.open).toBe(true);
  });

  it("forgets a row that unmounts, and its claim on being open", () => {
    const accordion = createRowAccordion();
    const a = fakeRow("a");
    const b = fakeRow("b");
    const drop = accordion.register(a);
    accordion.register(b);

    accordion.toggle("a");
    drop();
    accordion.toggle("b");
    expect(b.open).toBe(true);
    expect(a.applied).toEqual([true]);
  });

  it("ignores a row that reports no box", () => {
    const accordion = createRowAccordion();
    const a = fakeRow("a");
    const boxless = fakeRow("boxless");
    boxless.el = () => null;
    accordion.register(a);
    accordion.register(boxless);

    accordion.toggle("a");
    accordion.toggle("boxless");
    expect(boxless.open).toBe(false);
    expect(a.open).toBe(true);
  });

  it("reports heldOpen based on the recorded or toggled open row", () => {
    const accordion = createRowAccordion();
    expect(accordion.heldOpen?.("a")).toBeUndefined();

    accordion.recordOpen?.("a", true);
    expect(accordion.heldOpen?.("a")).toBe(true);
    expect(accordion.heldOpen?.("b")).toBe(false);

    accordion.recordOpen?.("a", false);
    expect(accordion.heldOpen?.("a")).toBeUndefined();
  });

  it("re-opens a row when called with open() if its DOM was closed externally", () => {
    const accordion = createRowAccordion();
    const a = fakeRow("a");
    accordion.register(a);

    accordion.open("a");
    expect(a.applied).toEqual([true]);

    // Closed DOM while key is held.
    a.open = false;
    accordion.open("a");
    expect(a.applied).toEqual([true, true]);
    expect(a.open).toBe(true);
  });
});
