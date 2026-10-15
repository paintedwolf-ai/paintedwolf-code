// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { createActivitySpanAccordion } from "./activity-span-accordion.ts";
import type { ActivitySpanRowHandle } from "./activity-span-accordion.ts";
import { transcriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";

function setOpen(shell: HTMLElement, open: boolean): void {
  shell.setAttribute("data-expanded", String(open));
}

function isOpen(shell: HTMLElement): boolean {
  return shell.getAttribute("data-expanded") === "true";
}

function fakeRow(key: string, domOpen = false) {
  const shell = document.createElement("div");
  shell.setAttribute("data-transcript-disclosure", "");
  const header = document.createElement("button");
  shell.append(header, document.createElement("div"));
  setOpen(shell, domOpen);
  const committed: boolean[] = [];
  const handle: ActivitySpanRowHandle = {
    key,
    disclosureKey: transcriptDisclosureKey.tool(key),
    shell: () => shell,
    header: () => header,
    applyOpen: (next: boolean) => {
      committed.push(next);
      setOpen(shell, next);
    },
  };
  return { handle, shell, committed };
}

describe("createActivitySpanAccordion", () => {
  it("keeps at most one row open across a sequence of toggles", () => {
    const accordion = createActivitySpanAccordion();
    const rows = ["a", "b", "c", "d"].map((k) => fakeRow(k));
    for (const row of rows) accordion.register(row.handle);

    const openKeys = () =>
      rows.filter((r) => isOpen(r.shell)).map((r) => r.handle.key);

    accordion.toggle("a");
    expect(openKeys()).toEqual(["a"]);
    accordion.toggle("c");
    expect(openKeys()).toEqual(["c"]);
    accordion.toggle("b");
    expect(openKeys()).toEqual(["b"]);
    accordion.toggle("d");
    expect(openKeys()).toEqual(["d"]);
  });

  it("clicking the open row closes it, leaving none open", () => {
    const accordion = createActivitySpanAccordion();
    const a = fakeRow("a");
    const b = fakeRow("b");
    accordion.register(a.handle);
    accordion.register(b.handle);

    accordion.toggle("a");
    expect(isOpen(a.shell)).toBe(true);
    accordion.toggle("a");
    expect(isOpen(a.shell)).toBe(false);
    expect(isOpen(b.shell)).toBe(false);
  });

  it("reopens a row whose DOM still reads open mid-close", () => {
    // Closing DOM state lags intent.
    const accordion = createActivitySpanAccordion();
    const a = fakeRow("a");
    const b = fakeRow("b");
    accordion.register(a.handle);
    accordion.register(b.handle);

    accordion.toggle("a");
    accordion.toggle("b");
    setOpen(a.shell, true);

    accordion.toggle("a");
    expect(a.committed[a.committed.length - 1]).toBe(true);
    expect(isOpen(b.shell)).toBe(false);
  });

  it("sweeps up a row restored open by the disclosure store", () => {
    // Restored state bypasses the accordion toggle.
    const accordion = createActivitySpanAccordion();
    const restored = fakeRow("a", true);
    const other = fakeRow("b");
    accordion.register(restored.handle);
    accordion.register(other.handle);

    accordion.toggle("b");
    expect(isOpen(restored.shell)).toBe(false);
    expect(isOpen(other.shell)).toBe(true);
  });

  it("does not resurrect intent after the open row unregisters", () => {
    const accordion = createActivitySpanAccordion();
    const a = fakeRow("a");
    const b = fakeRow("b");
    const unregisterA = accordion.register(a.handle);
    accordion.register(b.handle);

    accordion.toggle("a");
    unregisterA();
    setOpen(a.shell, false);

    accordion.toggle("b");
    expect(isOpen(b.shell)).toBe(true);
  });

  it("ignores toggles for rows it does not know", () => {
    const accordion = createActivitySpanAccordion();
    const a = fakeRow("a");
    accordion.register(a.handle);
    expect(() => accordion.toggle("missing")).not.toThrow();
    expect(isOpen(a.shell)).toBe(false);
  });
});
