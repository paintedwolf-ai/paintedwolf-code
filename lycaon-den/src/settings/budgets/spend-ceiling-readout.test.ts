import { describe, expect, it } from "vitest";
import type { CostSummary } from "../../api/types.ts";
import {
  raisedSpendCeilingUsd,
  SPEND_CEILING_WARN_RATIO,
  spendCeilingApproaching,
  spendCeilingReadout,
  type SpendCeilingReadout,
} from "./spend-ceiling-readout.ts";

const priced = (usd: number): CostSummary =>
  ({
    scope: "session",
    estimated_nano_usd: Math.round(usd * 1e9),
    estimate_coverage: "complete",
    token_totals: { prompt: 1, completion: 1 },
    coordinator: {
      estimated_nano_usd: Math.round(usd * 1e9),
      token_totals: { prompt: 1, completion: 1 },
    },
    workers: {
      estimated_nano_usd: 0,
      token_totals: { prompt: 0, completion: 0 },
      task_count: 0,
    },
  }) as CostSummary;

const lowerBound = (usd: number, causes: Partial<CostSummary>): CostSummary => ({
  ...priced(usd),
  estimate_coverage: "lower_bound",
  ...causes,
});

const unpriced = (): CostSummary =>
  ({
    scope: "session",
    estimated_nano_usd: 0,
    estimate_coverage: "unpriced",
    token_totals: { prompt: 10, completion: 5 },
    coordinator: {
      estimated_nano_usd: 0,
      token_totals: { prompt: 10, completion: 5 },
    },
    workers: {
      estimated_nano_usd: 0,
      token_totals: { prompt: 0, completion: 0 },
      task_count: 0,
    },
  }) as CostSummary;

describe("spendCeilingReadout", () => {
  it("is off when the ceiling is disabled", () => {
    expect(
      spendCeilingReadout({
        enabled: false,
        ceilingUsd: 5,
        summary: priced(1),
      }).kind,
    ).toBe("off");
  });

  it("shows unpriced honesty when enabled and nothing is priced", () => {
    const got = spendCeilingReadout({
      enabled: true,
      ceilingUsd: 5,
      summary: unpriced(),
    });
    expect(got).toEqual({
      kind: "unpriced",
      label: "not enforced (pricing unavailable)",
    });
  });

  it("shows spent of ceiling when complete", () => {
    const got = spendCeilingReadout({
      enabled: true,
      ceilingUsd: 5,
      summary: priced(2.5),
    });
    expect(got.kind).toBe("priced");
    if (got.kind === "priced") {
      expect(got.label).toBe("spent $2.50 of $5.00");
      expect(got.ratio).toBeCloseTo(0.5);
    }
  });

  it("says at least and names the cause when usage went unpriced", () => {
    const got = spendCeilingReadout({
      enabled: true,
      ceilingUsd: 5,
      summary: lowerBound(2.5, { unpriced_tokens: 900 }),
    });
    expect(got.kind).toBe("priced");
    if (got.kind === "priced") {
      expect(got.label).toBe(
        "spent at least $2.50 of $5.00 (900 tokens unpriced; actual charges may exceed this limit)",
      );
    }
  });

  it("still enforces when provider calls went unreported — lower bound, not off", () => {
    // Unreported calls leave enforcement of the priced lower bound active.
    const got = spendCeilingReadout({
      enabled: true,
      ceilingUsd: 5,
      summary: lowerBound(2.5, { unknown_calls: 2, unknown_charged_calls: 2 }),
    });
    expect(got.kind).toBe("priced");
    if (got.kind === "priced") {
      expect(got.label).toBe(
        "spent at least $2.50 of $5.00 (2 provider calls with incomplete usage; actual charges may exceed this limit)",
      );
      expect(got.ratio).toBeCloseTo(0.5);
    }
  });

  it("names both causes when unpriced tokens and unreported calls coexist", () => {
    const got = spendCeilingReadout({
      enabled: true,
      ceilingUsd: 5,
      summary: lowerBound(2.5, {
        unpriced_tokens: 900,
        unknown_calls: 1,
        unknown_charged_calls: 1,
      }),
    });
    expect(got.kind).toBe("priced");
    if (got.kind === "priced") {
      expect(got.label).toContain("900 tokens unpriced and 1 provider call with incomplete usage");
    }
  });

  it("never prefixes a sub-cent lower bound", () => {
    const got = spendCeilingReadout({
      enabled: true,
      ceilingUsd: 5,
      summary: lowerBound(0.002, { unknown_calls: 1, unknown_charged_calls: 1 }),
    });
    expect(got.kind).toBe("priced");
    if (got.kind === "priced") {
      expect(got.label).toBe(
        "spent less than $0.01 in priced usage of $5.00 (1 provider call with incomplete usage; actual charges may exceed this limit)",
      );
    }
  });

  it("reads whole when the host says the estimate is complete", () => {
    // Free local calls do not change coverage; the host has already decided.
    const got = spendCeilingReadout({
      enabled: true,
      ceilingUsd: 5,
      summary: { ...priced(2.5), unknown_calls: 2 },
    });
    expect(got.kind).toBe("priced");
    if (got.kind === "priced") {
      expect(got.label).toBe("spent $2.50 of $5.00");
    }
  });
});

describe("raisedSpendCeilingUsd", () => {
  it("doubles a positive ceiling above spent", () => {
    expect(raisedSpendCeilingUsd(5, 2)).toBe(10);
  });

  it("clears spent when doubling would stay below", () => {
    expect(raisedSpendCeilingUsd(5, 12)).toBe(13);
  });
});

describe("spendCeilingApproaching", () => {
  const priced = (ratio: number): Extract<SpendCeilingReadout, { kind: "priced" }> => ({
    kind: "priced",
    label: "x",
    ratio,
  });

  it("is false below the warn ratio", () => {
    expect(spendCeilingApproaching(priced(0.79))).toBe(false);
  });

  it("is true at and above the warn ratio while under the ceiling", () => {
    expect(SPEND_CEILING_WARN_RATIO).toBe(0.8);
    expect(spendCeilingApproaching(priced(0.8))).toBe(true);
    expect(spendCeilingApproaching(priced(0.99))).toBe(true);
  });

  it("uses the configured warning ratio instead of the default", () => {
    expect(spendCeilingApproaching({ ...priced(0.65), warningRatio: 0.6 })).toBe(true);
    expect(spendCeilingApproaching({ ...priced(0.65), warningRatio: 0.7 })).toBe(false);
  });

  it("is false when the ceiling is reached", () => {
    expect(spendCeilingApproaching(priced(1))).toBe(false);
  });

  it("is false for off, idle, and unpriced", () => {
    expect(spendCeilingApproaching({ kind: "off" })).toBe(false);
    expect(
      spendCeilingApproaching({ kind: "idle", label: "Waiting…" }),
    ).toBe(false);
    expect(
      spendCeilingApproaching({
        kind: "unpriced",
        label: "not enforced (pricing unavailable)",
      }),
    ).toBe(false);
  });
});
