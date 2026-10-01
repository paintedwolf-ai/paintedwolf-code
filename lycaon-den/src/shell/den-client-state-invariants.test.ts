import { describe, expect, it } from "vitest";
import {
  INVARIANT_CATALOG,
  assertInvariant,
} from "./den-client-state-invariants.ts";

describe("den client state invariants", () => {
  it("keeps the catalog populated across its protected surfaces", () => {
    const ids = new Set(INVARIANT_CATALOG.map((entry) => entry.id));
    expect(ids.size).toBe(INVARIANT_CATALOG.length);
    expect([...ids]).toEqual(
      expect.arrayContaining([
        "INV-NAV-01",
        "INV-FOCUS-KEY-01",
        "INV-CACHE-03",
        "INV-OPEN-01",
        "INV-PLACE-01",
        "INV-SPLIT-01",
        "INV-VIEW-01",
      ]),
    );
  });

  for (const entry of INVARIANT_CATALOG) {
    it(`${entry.id} (${entry.class})`, () => {
      assertInvariant(entry);
    });
  }
});
