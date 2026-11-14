import { describe, expect, it } from "vitest";
import {
  createTranscriptRunwayBudget,
  DEFAULT_TRANSCRIPT_ORD_DENSITY_PX,
  transcriptRunwayDensity,
  transcriptRunwayOrd,
} from "./transcript-runway.ts";

describe("transcriptRunwayBudget", () => {
  it("sizes unloaded history from measured density, refreshes on page landing, and holds beforePx steady across resident re-measurements", () => {
    const budget = createTranscriptRunwayBudget();

    // Estimates-only observation before any row is measured keeps the 84px default density.
    const unmeasured = budget.observe({
      unloaded: { before: 400, after: 0 },
      residentRowCount: 50,
      residentOrdSpan: 50,
      residentHeightPx: 5_000,
      measuredHeightPx: 0,
      measuredOrdSpan: 0,
      scrollOffsetPx: 38_000,
      viewportHeightPx: 800,
      following: true,
    });
    expect(unmeasured.densityPxPerOrd).toBe(DEFAULT_TRANSCRIPT_ORD_DENSITY_PX);
    expect(unmeasured.beforePx).toBe(400 * DEFAULT_TRANSCRIPT_ORD_DENSITY_PX);

    // First observation with measured rows refreshes density and beforePx.
    const firstMeasured = budget.observe({
      unloaded: { before: 400, after: 0 },
      residentRowCount: 100,
      residentOrdSpan: 100,
      residentHeightPx: 10_000,
      measuredHeightPx: 10_000,
      measuredOrdSpan: 100,
      scrollOffsetPx: 43_000,
      viewportHeightPx: 800,
      following: true,
    });
    expect(firstMeasured.densityPxPerOrd).toBe(100);
    expect(firstMeasured.beforePx).toBe(40_000);
    expect(firstMeasured.afterPx).toBe(0);

    // While reading with an older runway above, resident row re-measurement must not change beforePx.
    const afterRowMeasure = budget.observe({
      unloaded: { before: 400, after: 0 },
      residentRowCount: 100,
      residentOrdSpan: 100,
      residentHeightPx: 12_500,
      measuredHeightPx: 12_500,
      measuredOrdSpan: 100,
      scrollOffsetPx: 41_200,
      viewportHeightPx: 800,
      following: false,
    });
    expect(afterRowMeasure.densityPxPerOrd).toBe(100);
    expect(afterRowMeasure.beforePx).toBe(40_000);

    // Loading a compact older page (20px per message) changes `before` and refreshes density to 20.
    const afterPageLoad = budget.observe({
      unloaded: { before: 300, after: 0 },
      residentRowCount: 200,
      residentOrdSpan: 200,
      residentHeightPx: 4_000,
      measuredHeightPx: 4_000,
      measuredOrdSpan: 200,
      scrollOffsetPx: 6_200,
      viewportHeightPx: 800,
      following: false,
    });
    expect(afterPageLoad.densityPxPerOrd).toBe(20);
    expect(afterPageLoad.beforePx).toBe(6_000);

    // Reaching the start of history collapses the older runway to zero and refreshes density.
    const atStart = budget.observe({
      unloaded: { before: 0, after: 200 },
      residentRowCount: 300,
      residentOrdSpan: 300,
      residentHeightPx: 33_000,
      measuredHeightPx: 33_000,
      measuredOrdSpan: 300,
      scrollOffsetPx: 400,
      viewportHeightPx: 800,
      following: false,
    });
    expect(atStart.beforePx).toBe(0);
    expect(atStart.densityPxPerOrd).toBe(110);
    expect(atStart.afterPx).toBe(22_000);
  });

  it("freezes the targeted runway side while a seek landing is pending", () => {
    const budget = createTranscriptRunwayBudget();
    budget.observe({
      unloaded: { before: 400, after: 0 },
      residentRowCount: 50,
      residentOrdSpan: 50,
      residentHeightPx: 1_500,
      measuredHeightPx: 1_500,
      measuredOrdSpan: 50,
      scrollOffsetPx: 12_000,
      viewportHeightPx: 500,
      following: false,
    });
    budget.prepareLanding(3_000, 0, 1_500);
    const frozen = budget.observe({
      unloaded: { before: 400, after: 0 },
      residentRowCount: 50,
      residentOrdSpan: 50,
      residentHeightPx: 2_500,
      measuredHeightPx: 2_500,
      measuredOrdSpan: 50,
      scrollOffsetPx: 3_000,
      viewportHeightPx: 500,
      following: false,
    });
    expect(frozen.beforePx).toBe(12_000);

    // Once the sought page lands and `before` changes, the freeze clears.
    const landed = budget.observe({
      unloaded: { before: 100, after: 250 },
      residentRowCount: 50,
      residentOrdSpan: 50,
      residentHeightPx: 1_000,
      measuredHeightPx: 1_000,
      measuredOrdSpan: 50,
      scrollOffsetPx: 2_000,
      viewportHeightPx: 500,
      following: false,
    });
    expect(landed.densityPxPerOrd).toBe(20);
    expect(landed.beforePx).toBe(2_000);
  });

  it("falls back to the prior density when no resident rows are measured yet", () => {
    expect(
      transcriptRunwayDensity({
        measuredHeightPx: 0,
        measuredOrdSpan: 0,
      }),
    ).toBe(DEFAULT_TRANSCRIPT_ORD_DENSITY_PX);
    expect(
      transcriptRunwayDensity(
        {
          measuredHeightPx: 0,
          measuredOrdSpan: 50,
        },
        32,
      ),
    ).toBe(32);
  });

  it("maps scroll offsets in unloaded before and after runways to session ordinals", () => {
    const runway = { beforePx: 40_000, afterPx: 20_000, densityPxPerOrd: 100 };
    const bounds = { firstOrd: 1, lastOrd: 700 };
    const residentSpan = { firstOrd: 401, lastOrd: 500 };
    const unloaded = { before: 400, after: 200 };

    expect(
      transcriptRunwayOrd({
        offsetPx: 0,
        originPx: 0,
        runway,
        residentHeightPx: 10_000,
        residentSpan,
        bounds,
        unloaded,
      }),
    ).toBe(1);

    expect(
      transcriptRunwayOrd({
        offsetPx: 20_000,
        originPx: 0,
        runway,
        residentHeightPx: 10_000,
        residentSpan,
        bounds,
        unloaded,
      }),
    ).toBe(201);

    // Inside the resident window (40_000..50_000) returns undefined.
    expect(
      transcriptRunwayOrd({
        offsetPx: 45_000,
        originPx: 0,
        runway,
        residentHeightPx: 10_000,
        residentSpan,
        bounds,
        unloaded,
      }),
    ).toBeUndefined();

    // Halfway into the newer runway (50_000..70_000) maps to the middle of 501..700.
    expect(
      transcriptRunwayOrd({
        offsetPx: 60_000,
        originPx: 0,
        runway,
        residentHeightPx: 10_000,
        residentSpan,
        bounds,
        unloaded,
      }),
    ).toBe(601);
  });
});
