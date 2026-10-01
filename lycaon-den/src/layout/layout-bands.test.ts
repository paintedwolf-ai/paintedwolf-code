// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  LAYOUT_BAND_SCALES,
  bandStopPx,
  bandTokens,
  observeLayoutBand,
  resetLayoutBandsForTests,
  type BandScale,
} from "./layout-bands.ts";
import { resetSharedResizeObserverForTests } from "./shared-resize-observer.ts";
import { denSourceRoot, loadSourceCorpus } from "../test/source-corpus.ts";
import { SPLIT_MIN_HOST, STAGE_COL_MIN } from "../shell/stage-placement.ts";

const scales = Object.values(LAYOUT_BAND_SCALES) as readonly BandScale[];
const cssCorpus = loadSourceCorpus(denSourceRoot, { extensions: [".css"] });
const css = cssCorpus.files.map((file) => file.text).join("\n");

let emitResize: ((target: Element, width: number) => void) | undefined;

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", class implements ResizeObserver {
    constructor(callback: ResizeObserverCallback) {
      emitResize = (target, width) => callback([{
        target,
        contentRect: new DOMRectReadOnly(0, 0, width, 100),
        contentBoxSize: [{ inlineSize: width, blockSize: 100 }],
        borderBoxSize: [{ inlineSize: width, blockSize: 100 }],
        devicePixelContentBoxSize: [{ inlineSize: width, blockSize: 100 }],
      }], this);
    }
    observe() {}
    unobserve() {}
    disconnect() {}
  });
});

async function resizeBox(target: Element, width: number): Promise<void> {
  if (!emitResize) expect.fail("layout band observer was not installed");
  emitResize(target, width);
  await Promise.resolve();
}

afterEach(() => {
  resetLayoutBandsForTests();
  resetSharedResizeObserverForTests();
  emitResize = undefined;
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe("band model", () => {
  it("resolves a max stop the way `max-width` does", () => {
    const scale: BandScale = {
      attribute: "data-x",
      stops: [{ token: "max-100", edge: "max", px: 100 }],
    };
    expect(bandTokens(scale, 99, 16)).toBe("max-100");
    expect(bandTokens(scale, 100, 16)).toBe("max-100");
    // `max-width: 100px` does not match a fractional 100.5.
    expect(bandTokens(scale, 100.5, 16)).toBe("");
  });

  it("resolves a min stop the way `min-width` does", () => {
    const scale: BandScale = {
      attribute: "data-x",
      stops: [{ token: "min-100", edge: "min", px: 100 }],
    };
    expect(bandTokens(scale, 99.5, 16)).toBe("");
    expect(bandTokens(scale, 100, 16)).toBe("min-100");
  });

  it("resolves rem stops against the passed root font size", () => {
    const scale: BandScale = {
      attribute: "data-x",
      stops: [{ token: "max-32rem", edge: "max", rem: 32 }],
    };
    expect(bandTokens(scale, 448, 14)).toBe("max-32rem");
    expect(bandTokens(scale, 449, 14)).toBe("");
    // A larger reading scale moves the stop with the root font size.
    expect(bandTokens(scale, 449, 16)).toBe("max-32rem");
  });

  it("adds px and rem in one stop, mirroring calc()", () => {
    expect(bandStopPx({ token: "t", edge: "max", px: 200, rem: 5 }, 14)).toBe(270);
  });

  it("emits every satisfied token in catalog order", () => {
    const tokens = bandTokens(LAYOUT_BAND_SCALES.filesEditor, 300, 16);
    expect(tokens).toBe("max-320 max-520 max-620");
  });

  it("spells each px token after the width it resolves to", () => {
    // Pixel tokens encode their resolved threshold.
    for (const scale of scales) {
      for (const stop of scale.stops) {
        if (stop.rem !== undefined) continue;
        expect(stop.token).toBe(`${stop.edge}-${stop.px}`);
      }
    }
  });

  it("keeps the split and Files seams on their shared constants", () => {
    expect(LAYOUT_BAND_SCALES.stageHost.stops.map((stop) => stop.px)).toEqual([
      SPLIT_MIN_HOST - 1,
      SPLIT_MIN_HOST,
    ]);
    expect(LAYOUT_BAND_SCALES.stageColumn.stops.map((stop) => stop.px)).toEqual([
      STAGE_COL_MIN - 1,
    ]);
  });
});

describe("band publication", () => {
  it("writes the attribute from the observed box", async () => {
    const el = document.createElement("div");
    document.body.append(el);
    observeLayoutBand(el, LAYOUT_BAND_SCALES.browseStage);
    expect(el.getAttribute("data-browse-band")).toBeNull();
    await resizeBox(el, 500);
    expect(el.getAttribute("data-browse-band")).toBe(
      "max-560 max-620 max-700 max-850",
    );
    await resizeBox(el, 650);
    expect(el.getAttribute("data-browse-band")).toBe("max-700 max-850");
  });

  it("stops publishing once disposed", async () => {
    const el = document.createElement("div");
    document.body.append(el);
    const dispose = observeLayoutBand(el, LAYOUT_BAND_SCALES.stageHost);
    await resizeBox(el, 500);
    expect(el.getAttribute("data-host-band")).toBe(`max-${SPLIT_MIN_HOST - 1}`);
    if (!emitResize) expect.fail("layout band observer was not installed");
    emitResize(el, 1000);
    dispose();
    await Promise.resolve();
    expect(el.getAttribute("data-host-band")).toBe(`max-${SPLIT_MIN_HOST - 1}`);
    await resizeBox(el, 1000);
    expect(el.getAttribute("data-host-band")).toBe(`max-${SPLIT_MIN_HOST - 1}`);
  });
});

describe("band and stylesheet agree", () => {
  const used = new Set(
    [...css.matchAll(/\[(data-[\w-]*band)~="([^"]+)"\]/g)].map(
      (match) => `${match[1]}~${match[2]}`,
    ),
  );

  it("declares every token the stylesheet matches", () => {
    const declared = new Set(
      scales.flatMap((scale) =>
        scale.stops.map((stop) => `${scale.attribute}~${stop.token}`),
      ),
    );
    expect([...used].filter((token) => !declared.has(token))).toEqual([]);
  });

  it("leaves no stop the stylesheet never asks for", () => {
    const unused = scales.flatMap((scale) =>
      scale.stops
        .filter((stop) => !used.has(`${scale.attribute}~${stop.token}`))
        .map((stop) => `${scale.attribute}~${stop.token}`),
    );
    expect(unused).toEqual([]);
  });

  it("uses width bands for whole regions", () => {
    for (const region of [
      "den-stage-host",
      "den-stage-column",
      "den-chat-column",
      "den-browse-stage",
      "den-files-editor",
      "den-files-tree-pane",
      "den-chat-stage",
      "den-diffs-page",
    ]) {
      expect(css).not.toContain(`container-name: ${region}`);
      expect(css).not.toContain(`@container ${region} `);
    }

    expect(css).not.toMatch(/@container\s*\(/);
  });

  it("keeps the band attribute out of the cascade", () => {
    // Band attributes contribute no specificity inside :where().
    const whereRanges: Array<[number, number]> = [];
    const where = /:where\(/g;
    for (let m = where.exec(css); m; m = where.exec(css)) {
      let depth = 1;
      let end = m.index + m[0].length;
      for (; end < css.length && depth > 0; end += 1) {
        if (css[end] === "(") depth += 1;
        else if (css[end] === ")") depth -= 1;
      }
      whereRanges.push([m.index + m[0].length, end]);
    }
    const unwrapped: string[] = [];
    const band = /\[data-[\w-]*band~="[^"]+"\]/g;
    for (let m = band.exec(css); m; m = band.exec(css)) {
      const at = m.index;
      if (!whereRanges.some(([start, end]) => at >= start && at < end)) {
        unwrapped.push(m[0]);
      }
    }
    expect(unwrapped).toEqual([]);
  });
});
