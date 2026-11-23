import { CHAT_GAPS_PX } from "./chat-gap-defaults.ts";
import { afterEach, describe, expect, it } from "vitest";
import { DEFAULT_TRANSCRIPT_SPACING, TRANSCRIPT_SPACING, TRANSCRIPT_ROOT_REM_PX, setTranscriptSpacing, transcriptGeometryKey, transcriptInteriorSignature, transcriptSeamPx, transcriptSpacing, transcriptSpacingProperty, transcriptSpacingStyles } from "./transcript-spacing.ts";
import { presentedVisualsHeight, transcriptRowContentEstimate } from "./transcript-row-content-estimate.ts";
import { denSourceRoot, loadSourceCorpus } from "../../../test/source-corpus.ts";

const cssCorpus = loadSourceCorpus(denSourceRoot, { extensions: [".css"] });
const css = cssCorpus.files.map((file) => file.text).join("\n");

afterEach(() => setTranscriptSpacing(DEFAULT_TRANSCRIPT_SPACING));
describe("transcript spacing", () => {
  it("publishes every declared length to CSS with its original unit", () => {
    const styles = transcriptSpacingStyles(DEFAULT_TRANSCRIPT_SPACING);
    for (const [name, spec] of Object.entries(TRANSCRIPT_SPACING)) {
      expect(styles[transcriptSpacingProperty(name as keyof typeof TRANSCRIPT_SPACING)]).toBe(`${spec.value}${spec.unit}`);
    }
    expect(styles["--transcript-paragraph-gap"]).toBe("0.9em");
  });
  it("carries every seam rung in rem so it follows the text scale", () => {
    for (const rung of ["partGap", "itemGap", "messageGap"] as const) {
      expect(TRANSCRIPT_SPACING[rung].unit, rung).toBe("rem");
    }
  });
  it("resolves a seam against the live root size, and the first row to nothing", () => {
    for (const scale of [1, 2]) {
      const rem = TRANSCRIPT_ROOT_REM_PX * scale;
      const message = CHAT_GAPS_PX.aroundMessages * scale;
      const reserve = 4 + (1.5 + DEFAULT_TRANSCRIPT_SPACING.messageTimeGap) * rem;
      expect(transcriptSeamPx("item", DEFAULT_TRANSCRIPT_SPACING, rem)).toBeCloseTo(CHAT_GAPS_PX.betweenItems * scale);
      expect(transcriptSeamPx("message", DEFAULT_TRANSCRIPT_SPACING, rem)).toBeCloseTo(message);
      expect(transcriptSeamPx("message-reserved", DEFAULT_TRANSCRIPT_SPACING, rem)).toBeCloseTo(Math.max(0, message - reserve));
    }
    expect(transcriptSeamPx(undefined, DEFAULT_TRANSCRIPT_SPACING, TRANSCRIPT_ROOT_REM_PX)).toBe(0);
  });
  it("clamps the seam when the reserved bar fills the gap", () => {
    for (const messageGap of [0, 1]) {
      expect(transcriptSeamPx("message-reserved", { ...DEFAULT_TRANSCRIPT_SPACING, messageGap }, 14)).toBe(0);
    }
  });
  it("retains interior measurements when only row placement changes", () => {
    const before = transcriptInteriorSignature(DEFAULT_TRANSCRIPT_SPACING);
    const next = { ...DEFAULT_TRANSCRIPT_SPACING, itemGap: 1.9375, messageGap: 4 };
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
    expect(() => setTranscriptSpacing({ ...previous, itemGap: Number.NaN })).toThrow();
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
  it("agrees bidirectionally with --transcript-* custom properties in the CSS corpus", () => {
    const declared = new Set(
      Object.keys(TRANSCRIPT_SPACING).map((name) =>
        transcriptSpacingProperty(name as keyof typeof TRANSCRIPT_SPACING),
      ),
    );
    const used = new Set(
      [...css.matchAll(/var\(\s*(--transcript-[\w-]+)/g)].map((match) => match[1]!),
    );
    expect([...used].filter((prop) => !declared.has(prop))).toEqual([]);
    expect([...declared].filter((prop) => !used.has(prop))).toEqual([]);
  });
});
