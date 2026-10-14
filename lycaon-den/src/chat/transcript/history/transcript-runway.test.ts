import { describe, expect, it } from "vitest";
import {
  createTranscriptRunwayBudget,
  DEFAULT_TRANSCRIPT_ORD_DENSITY_PX,
  transcriptRunwayDensity,
  transcriptRunwayOrd,
} from "./transcript-runway.ts";

describe("transcriptRunwayBudget", () => {
  it("sizes unloaded history from resident density and holds beforePx steady across resident re-measurements", () => {
    const budget = createTranscriptRunwayBudget();

    // Initial hydration at the live tail with 400 unloaded older ordinals and 100 resident rows (10_000px).
    const initial = budget.observe({
      unloaded: { before: 400, after: 0 },
      residentRowCount: 100,
      residentOrdSpan: 100,
      residentHeightPx: 10_000,
      scrollOffsetPx: 43_000,
      viewportHeightPx: 800,
      following: true,
    });
    expect(initial.densityPxPerOrd).toBe(100);
    expect(initial.beforePx).toBe(40_000);
    expect(initial.afterPx).toBe(0);

    // While reading with an older runway above, resident row re-measurement must not change beforePx.
    const afterRowMeasure = budget.observe({
      unloaded: { before: 400, after: 0 },
      residentRowCount: 100,
      residentOrdSpan: 100,
      residentHeightPx: 12_500,
      scrollOffsetPx: 41_200,
      viewportHeightPx: 800,
      following: false,
    });
    expect(afterRowMeasure.beforePx).toBe(40_000);

    // Loading an older page shrinks the unloaded count and reconciles beforePx at the locked density.
    const afterPageLoad = budget.observe({
      unloaded: { before: 300, after: 0 },
      residentRowCount: 200,
      residentOrdSpan: 200,
      residentHeightPx: 22_000,
      scrollOffsetPx: 31_200,
      viewportHeightPx: 800,
      following: false,
    });
    expect(afterPageLoad.beforePx).toBe(30_000);

    // Reaching the start of history collapses the older runway to zero.
    const atStart = budget.observe({
      unloaded: { before: 0, after: 200 },
      residentRowCount: 300,
      residentOrdSpan: 300,
      residentHeightPx: 33_000,
      scrollOffsetPx: 400,
      viewportHeightPx: 800,
      following: false,
    });
    expect(atStart.beforePx).toBe(0);
    expect(atStart.densityPxPerOrd).toBe(110);
    expect(atStart.afterPx).toBe(22_000);
  });

  it("falls back to the default density when no resident rows are measured yet", () => {
    expect(
      transcriptRunwayDensity({
        residentRowCount: 0,
        residentOrdSpan: 0,
        residentHeightPx: 0,
      }),
    ).toBe(DEFAULT_TRANSCRIPT_ORD_DENSITY_PX);
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
