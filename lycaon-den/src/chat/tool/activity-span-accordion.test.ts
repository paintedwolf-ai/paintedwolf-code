// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { createActivitySpanAccordion } from "./activity-span-accordion.ts";
import type { ActivitySpanRowHandle } from "./activity-span-accordion.ts";
import { transcriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";

function fakeRow(key: string, domOpen = false) {
  const details = document.createElement("details");
  const summary = document.createElement("summary");
  details.append(summary, document.createElement("div"));
  details.open = domOpen;
  const committed: boolean[] = [];
  const handle: ActivitySpanRowHandle = {
    key,
    disclosureKey: transcriptDisclosureKey.tool(key),
    details: () => details,
    summary: () => summary,
    applyOpen: (next: boolean) => {
      committed.push(next);
      details.open = next;
    },
  };
  return { handle, details, committed };
}

describe("createActivitySpanAccordion", () => {
  it("keeps at most one row open across a sequence of toggles", () => {
    const accordion = createActivitySpanAccordion();
    const rows = ["a", "b", "c", "d"].map((k) => fakeRow(k));
    for (const row of rows) accordion.register(row.handle);

    const openKeys = () =>
      rows.filter((r) => r.details.open).map((r) => r.handle.key);

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
    expect(a.details.open).toBe(true);
    accordion.toggle("a");
    expect(a.details.open).toBe(false);
    expect(b.details.open).toBe(false);
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
    a.details.open = true;

    accordion.toggle("a");
    expect(a.committed[a.committed.length - 1]).toBe(true);
    expect(b.details.open).toBe(false);
  });

  it("sweeps up a row restored open by the disclosure store", () => {
    // Restored state bypasses the accordion toggle.
    const accordion = createActivitySpanAccordion();
    const restored = fakeRow("a", true);
    const other = fakeRow("b");
    accordion.register(restored.handle);
    accordion.register(other.handle);

    accordion.toggle("b");
    expect(restored.details.open).toBe(false);
    expect(other.details.open).toBe(true);
  });

  it("does not resurrect intent after the open row unregisters", () => {
    const accordion = createActivitySpanAccordion();
    const a = fakeRow("a");
    const b = fakeRow("b");
    const unregisterA = accordion.register(a.handle);
    accordion.register(b.handle);

    accordion.toggle("a");
    unregisterA();
    a.details.open = false;

    accordion.toggle("b");
    expect(b.details.open).toBe(true);
  });

  it("ignores toggles for rows it does not know", () => {
    const accordion = createActivitySpanAccordion();
    const a = fakeRow("a");
    accordion.register(a.handle);
    expect(() => accordion.toggle("missing")).not.toThrow();
    expect(a.details.open).toBe(false);
  });
});
