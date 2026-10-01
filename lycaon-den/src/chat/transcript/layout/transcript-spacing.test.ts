import { afterEach, describe, expect, it } from "vitest";
import { DEFAULT_TRANSCRIPT_SPACING, TRANSCRIPT_SPACING, TRANSCRIPT_ROOT_REM_PX, setTranscriptSpacing, transcriptGeometryKey, transcriptInteriorSignature, transcriptSeamPx, transcriptSpacing, transcriptSpacingProperty, transcriptSpacingStyles } from "./transcript-spacing.ts";
import { presentedVisualsHeight, transcriptRowContentEstimate } from "./transcript-row-content-estimate.ts";

afterEach(() => setTranscriptSpacing(DEFAULT_TRANSCRIPT_SPACING));
describe("transcript spacing", () => {
  it("publishes every declared length to CSS with its original unit", () => {
    const styles = transcriptSpacingStyles(DEFAULT_TRANSCRIPT_SPACING);
    for (const [name, spec] of Object.entries(TRANSCRIPT_SPACING)) {
      expect(styles[transcriptSpacingProperty(name as keyof typeof TRANSCRIPT_SPACING)]).toBe(`${spec.value}${spec.unit}`);
    }
    expect(styles["--transcript-row-gap"]).toBe("0.8571rem");
    expect(styles["--transcript-paragraph-gap"]).toBe("0.65em");
  });
  it("carries every seam rung in rem so it follows the text scale", () => {
    for (const rung of ["partGap", "rowGap", "sectionGap", "turnGap"] as const) {
      expect(TRANSCRIPT_SPACING[rung].unit, rung).toBe("rem");
    }
    // The rungs climb; two claims on one seam resolve by precedence, never by adding up.
    expect(DEFAULT_TRANSCRIPT_SPACING.partGap).toBeLessThan(DEFAULT_TRANSCRIPT_SPACING.rowGap);
    expect(DEFAULT_TRANSCRIPT_SPACING.rowGap).toBeLessThan(DEFAULT_TRANSCRIPT_SPACING.sectionGap);
    expect(DEFAULT_TRANSCRIPT_SPACING.sectionGap).toBeLessThan(DEFAULT_TRANSCRIPT_SPACING.turnGap);
  });
  it("resolves a seam against the live root size, and the first row to nothing", () => {
    const at = (remPx: number) => [
      transcriptSeamPx("row", DEFAULT_TRANSCRIPT_SPACING, remPx),
      transcriptSeamPx("section", DEFAULT_TRANSCRIPT_SPACING, remPx),
      transcriptSeamPx("turn", DEFAULT_TRANSCRIPT_SPACING, remPx),
    ].map((px) => Math.round(px));
    expect(at(TRANSCRIPT_ROOT_REM_PX)).toEqual([12, 20, 72]);
    expect(at(TRANSCRIPT_ROOT_REM_PX * 2)).toEqual([24, 40, 144]);
    expect(transcriptSeamPx(undefined, DEFAULT_TRANSCRIPT_SPACING, TRANSCRIPT_ROOT_REM_PX)).toBe(0);
  });
  it("retains interior measurements when only row placement changes", () => {
    const before = transcriptInteriorSignature(DEFAULT_TRANSCRIPT_SPACING);
    const next = { ...DEFAULT_TRANSCRIPT_SPACING, rowGap: 1.9375, sectionGap: 2.5, turnGap: 4 };
    expect(transcriptInteriorSignature(next)).toBe(before);
  });
  it("changes cache identity for every interior length", () => {
    const before = transcriptInteriorSignature(DEFAULT_TRANSCRIPT_SPACING);
    for (const [key, spec] of Object.entries(TRANSCRIPT_SPACING)) {
      if (spec.affects !== "row") continue;
      expect(transcriptInteriorSignature({ ...DEFAULT_TRANSCRIPT_SPACING, [key]: spec.value + 1 }), key).not.toBe(before);
    }
  });
  it("estimates from the same paragraph spacing and bubble padding that CSS receives", () => {
    const metrics = { widthPx: 700, remPx: 16, bodyPx: 16 };
    const user = { kind: "user" as const, key: "u", text: "Short" };
    const before = transcriptRowContentEstimate(user, metrics, []);
    const taller = DEFAULT_TRANSCRIPT_SPACING.userPaddingY + 0.625;
    expect(transcriptRowContentEstimate(user, { ...metrics, spacing: { ...DEFAULT_TRANSCRIPT_SPACING, userPaddingY: taller } }, [])).toBe(before! + 20);
    const answer = { kind: "assistant" as const, key: "a", text: "First\n\nSecond" };
    expect(transcriptRowContentEstimate(answer, { ...metrics, spacing: { ...DEFAULT_TRANSCRIPT_SPACING, paragraphGap: 2 } }, [])).toBeGreaterThan(transcriptRowContentEstimate(answer, metrics, [])!);
  });
  it("rejects invalid lengths and avoids unchanged publications", () => {
    const previous = transcriptSpacing();
    setTranscriptSpacing({ ...previous });
    expect(transcriptSpacing()).toBe(previous);
    expect(() => setTranscriptSpacing({ ...previous, rowGap: Number.NaN })).toThrow();
    expect(() => setTranscriptSpacing({ ...previous, userPaddingY: -1 })).toThrow();
    expect(transcriptSpacing()).toBe(previous);
  });
  it("keeps visual estimates finite when strip spacing or width is zero", () => {
    const visuals = [{ kind: "unsized" as const }, { kind: "unsized" as const }];
    for (const stripWidth of [0, 840]) {
      const spacing = { ...DEFAULT_TRANSCRIPT_SPACING, stripWidth, stripGap: 0, stripColumn: 0 };
      expect(Number.isFinite(presentedVisualsHeight(visuals, 700, 16, spacing))).toBe(true);
    }
  });
  it("keys fractional width, typography and interior layout together", () => {
    const dimensions = { widthPx: 700.125, remPx: 16, bodyPx: 14 };
    const signature = transcriptInteriorSignature(DEFAULT_TRANSCRIPT_SPACING);
    const typography = { uiFont: "ui", monoFont: "mono", scale: 1, revision: "renderer" };
    const key = transcriptGeometryKey(dimensions, signature, typography);
    expect(transcriptGeometryKey({ ...dimensions, widthPx: 700.25 }, signature, typography)).not.toBe(key);
    expect(transcriptGeometryKey(dimensions, signature, { ...typography, uiFont: "other" })).not.toBe(key);
    expect(transcriptGeometryKey(dimensions, signature, { ...typography, scale: 1.2 })).not.toBe(key);
    expect(transcriptGeometryKey(dimensions, signature, { ...typography, revision: "next" })).not.toBe(key);
  });
});
