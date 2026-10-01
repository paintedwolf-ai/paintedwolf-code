import { describe, expect, it } from "vitest";
import {
  assertCanonicalOrder,
  loadOrderingFixture,
} from "./assert-canonical-order.ts";

describe("ordering golden fixture", () => {
  const fixture = loadOrderingFixture();

  it("canonical_order rows are non-decreasing on (ord, sub)", () => {
    const order = fixture.canonical_order;
    for (let i = 1; i < order.length; i++) {
      const prev = order[i - 1]!;
      const cur = order[i]!;
      expect(prev.ord).toBeLessThanOrEqual(cur.ord);
      if (prev.ord === cur.ord) {
        expect(prev.sub).toBeLessThan(cur.sub);
      }
    }
  });

  it("backing rows that are not chat items are excluded from canonical_order", () => {
    const keys = new Set(fixture.canonical_order.map((row) => row.key));
    // worker_summary patches into the canonical worker card; internal/compacted
    // are visibility-filtered. Transcript-visible workflow_boundary rows are
    // ord-ordered timeline markers (m2 start, m16 end).
    expect(keys.has("m2")).toBe(true);
    expect(keys.has("m16")).toBe(true);
    expect(keys.has("m11")).toBe(false);
    expect(keys.has("m17")).toBe(false);
    expect(keys.has("m18")).toBe(false);
    const ids = new Set(fixture.messages.map((m) => m.id));
    for (const id of ["m2", "m11", "m16", "m17", "m18"]) {
      expect(ids.has(id), `backing row ${id} must be present in messages`).toBe(true);
    }
  });

  it("synthesized/child items derive ord from constituents/backing", () => {
    const byKind = new Map(fixture.canonical_order.map((row) => [row.kind, row]));
    // activity_span ord = min constituent ord (m4=4, m5=5 → 4).
    expect(byKind.get("activity_span")?.ord).toBe(4);
    // worker_file_edit ord = the promotion result row (m19=19).
    expect(byKind.get("worker_file_edit")?.ord).toBe(19);
    // inline checkpoint inherits its backing tool row's ord (m10=10).
    expect(byKind.get("checkpoint")?.ord).toBe(10);
  });

  it("every backing message carries a unique monotonic ord (creation order)", () => {
    const ords = fixture.messages.map((m) => m.ord);
    expect([...ords].sort((a, b) => a - b)).toEqual(ords);
    expect(new Set(ords).size).toBe(ords.length);
  });

  it("assertCanonicalOrder accepts the canonical order (self-consistent)", () => {
    const res = assertCanonicalOrder(fixture.canonical_order, fixture.canonical_order);
    expect(res.ok).toBe(true);
    expect(res.diff).toBeNull();
  });

  it("assertCanonicalOrder sorts a permuted order back to canonical (order == ord, not arrival)", () => {
    const permuted = [...fixture.canonical_order].reverse();
    const res = assertCanonicalOrder(permuted, fixture.canonical_order);
    expect(res.ok).toBe(true);
  });

  it("assertCanonicalOrder rejects a missing item", () => {
    const missing = fixture.canonical_order.slice(1);
    const res = assertCanonicalOrder(missing, fixture.canonical_order);
    expect(res.ok).toBe(false);
    expect(res.diff).toMatch(/length/);
  });

  it("assertCanonicalOrder rejects a wrong key at a position", () => {
    const wrong = fixture.canonical_order.map((row, idx) =>
      idx === 0 ? { ...row, key: "WRONG" } : row,
    );
    const res = assertCanonicalOrder(wrong, fixture.canonical_order);
    expect(res.ok).toBe(false);
    expect(res.diff).toMatch(/index 0/);
  });

  it("draft_versions carries the reject version (Variant B: draft_version_count=1)", () => {
    expect(fixture.draft_versions).toHaveLength(1);
    const v = fixture.draft_versions[0]!;
    expect(v.version_index).toBe(1);
    expect(v.outcome_code).toBe("ungrounded");
    const draft = fixture.messages.find((m) => m.id === "m14")!;
    expect(draft.draft_version_count).toBe(1);
    expect(draft.draft_status).toBe("live");
  });
});
