/** Enforces icon-slot geometry, paint, and coverage invariants. */
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  ICON_SLOTS,
  ICON_VIEWBOX,
} from "../contributions/theme-vocabulary.generated.ts";
import { loadTypeScriptCorpus } from "../test/source-corpus.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../test/vitest-timeouts.ts";

const DEN_SRC = join(dirname(fileURLToPath(import.meta.url)), "..");
const HOST_GLYPHS_FILE = join(DEN_SRC, "contributions", "host-glyphs.tsx");

function rel(path: string): string {
  return path.slice(DEN_SRC.length + 1);
}

const componentFiles = loadTypeScriptCorpus(DEN_SRC, { excludeTests: true }).files
  .filter((file) => file.path.endsWith(".tsx"))
  .map((file) => file.path);
const glyphSource = readFileSync(HOST_GLYPHS_FILE, "utf8");

const FRAME = Number(ICON_VIEWBOX.split(" ")[3]);
const SLACK = 0.02;

/** SVG path argument counts. */
const ARITY: Record<string, number> = {
  M: 2, L: 2, T: 2, H: 1, V: 1, C: 6, S: 4, Q: 4, A: 7, Z: 0,
};

/** Yields absolute endpoints and control points. Arc extents remain lower bounds. */
function* pathPoints(d: string): Generator<[number, number]> {
  const tokens = d.match(/[A-Za-z]|-?(?:\d*\.\d+|\d+)/g) ?? [];
  let [x, y, startX, startY] = [0, 0, 0, 0];
  let cmd = "M";
  let i = 0;
  while (i < tokens.length) {
    if (/[A-Za-z]/.test(tokens[i]!)) {
      cmd = tokens[i]!;
      i += 1;
    }
    const upper = cmd.toUpperCase();
    const relative = cmd !== upper;
    const arity = ARITY[upper] ?? 2;
    if (upper === "Z") {
      [x, y] = [startX, startY];
      continue;
    }
    const args = tokens.slice(i, i + arity).map(Number);
    if (args.length < arity) return;
    i += arity;
    if (upper === "H") {
      x = relative ? x + args[0]! : args[0]!;
    } else if (upper === "V") {
      y = relative ? y + args[0]! : args[0]!;
    } else {
      // Pairs, minus the five leading non-positional numbers of an arc.
      const pairs = upper === "A" ? [args.slice(5)] : chunk(args, 2);
      for (const [dx, dy] of pairs) {
        const px = relative ? x + dx! : dx!;
        const py = relative ? y + dy! : dy!;
        yield [px, py];
      }
      const last = pairs[pairs.length - 1]!;
      x = relative ? x + last[0]! : last[0]!;
      y = relative ? y + last[1]! : last[1]!;
    }
    yield [x, y];
    // A repeated argument group after `M` is an implicit `L`.
    if (upper === "M") {
      [startX, startY] = [x, y];
      cmd = relative ? "l" : "L";
    }
  }
}

function chunk(xs: number[], size: number): number[][] {
  const out: number[][] = [];
  for (let i = 0; i < xs.length; i += size) out.push(xs.slice(i, i + size));
  return out;
}

function outside(v: number): boolean {
  return v < -SLACK || v > FRAME + SLACK;
}

function* outsideFrame(source: string): Generator<string> {
  for (const [, d] of source.matchAll(/\bd="([^"]*)"/g)) {
    for (const [px, py] of pathPoints(d!)) {
      if (outside(px) || outside(py)) yield `d="${d}" → (${px}, ${py})`;
    }
  }
  // Attributes may use quoted or JSX numeric literals.
  const attrs = (tag: string) =>
    [...source.matchAll(new RegExp(`<${tag}[\\s\\n]([^/>]*)/?>`, "g"))].map((m) =>
      Object.fromEntries(
        [...m[1]!.matchAll(/([a-z-]+)=(?:"(-?[\d.]+)"|\{(-?[\d.]+)\})/g)].map(
          (a) => [a[1]!, Number(a[2] ?? a[3])],
        ),
      ),
    );
  // Dynamic Dot geometry is checked at its call sites.
  const literal = (shape: Record<string, number>, keys: string[]) =>
    keys.every((k) => Number.isFinite(shape[k]));

  for (const c of attrs("circle")) {
    if (!literal(c, ["cx", "cy", "r"])) continue;
    const [cx, cy, r] = [c.cx!, c.cy!, c.r!];
    if ([cx - r, cx + r, cy - r, cy + r].some(outside)) {
      yield `<circle cx=${cx} cy=${cy} r=${r}>`;
    }
  }
  for (const box of attrs("rect")) {
    if (!literal(box, ["x", "y", "width", "height"])) continue;
    const [x, y, w, h] = [box.x!, box.y!, box.width!, box.height!];
    if ([x, y, x + w, y + h].some(outside)) {
      yield `<rect x=${x} y=${y} width=${w} height=${h}>`;
    }
  }
  // Dot call sites carry center coordinates only.
  for (const dot of attrs("Dot")) {
    if (!literal(dot, ["x", "y"])) continue;
    if ([dot.x!, dot.y!].some(outside)) yield `<Dot x=${dot.x} y=${dot.y}>`;
  }
}

/** Each slot's drawing, whitespace-collapsed, keyed by slot id. */
function glyphDrawings(source: string): Map<string, string> {
  const body = source.slice(source.indexOf("export const HOST_GLYPHS"));
  const entry = /\n {2}"([a-z0-9-]+)": \(\) => (\([\s\S]*?\n {2}\)|<[^\n]*?\/>),/g;
  const out = new Map<string, string>();
  for (const [, slot, drawing] of body.matchAll(entry)) {
    out.set(slot!, drawing!.replace(/\s+/g, " ").trim());
  }
  return out;
}

describe("icon slot coverage", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
  it("leaves no glyph outside the slot system", () => {
    const EXEMPT = new Set([
      // Slot renderer.
      "components/primitives/ThemeIcon.tsx",
      // Fixed product mark.
      "components/primitives/BrandLockup.tsx",
      // Marks computed from data, not drawn glyphs.
      "components/chatview/ContextRing.tsx",
      "components/project/ProjectCostComposition.tsx",
      "files/walk/StepBar.tsx",
      // Layout-scale teaching arrow.
      "components/onboarding/OnboardingGate.tsx",
    ]);
    const offenders: string[] = [];
    for (const file of componentFiles) {
      const name = rel(file);
      if (EXEMPT.has(name)) continue;
      const count = (readFileSync(file, "utf8").match(/<svg[\s>]/g) ?? []).length;
      if (count > 0) offenders.push(`${name} (${count})`);
    }
    expect(
      offenders,
      "route these through ThemeIcon and give each one a slot — a glyph " +
        `written inline is one no theme can ever reach:\n${offenders.join("\n")}`,
    ).toEqual([]);
  });

  it("draws every host glyph inside the frame", () => {
    const over = [...outsideFrame(glyphSource)];
    expect(
      over,
      `authored on a grid the frame is not: ${ICON_VIEWBOX} clips anything ` +
        "past it, and scaling it to fit would scale the stroke with it. " +
        `Redraw on the ${FRAME}-unit grid:\n${over.join("\n")}`,
    ).toEqual([]);
  });

  it("leaves paint and weight to the slot", () => {
    const banned = [...glyphSource.matchAll(/\s(fill|stroke|stroke-width)=/g)];
    expect(
      banned.map((m) => m[1]),
      "a glyph that names its own paint is one the slot's paint mode and " +
        "stroke weight no longer govern — and one a theme's stroke treatment " +
        "cannot reach. Drop the attribute and let the slot decide.",
    ).toEqual([]);
  });

  it("draws each slot differently", () => {
    const drawings = glyphDrawings(glyphSource);
    // A parser that silently matches nothing would pass the duplicate check.
    expect(
      [...drawings.keys()].sort(),
      "the entry parser no longer sees every slot, so the duplicate check " +
        "below proves nothing. Fix the parser to match how the glyphs are written.",
    ).toEqual(Object.keys(ICON_SLOTS).sort());
    const byDrawing = new Map<string, string[]>();
    for (const [slot, drawing] of drawings) {
      byDrawing.set(drawing, [...(byDrawing.get(drawing) ?? []), slot]);
    }
    const shared = [...byDrawing.values()].filter((slots) => slots.length > 1);
    expect(
      shared.map((slots) => slots.join(" ≡ ")),
      "two slots draw the same glyph, so one meaning has no mark of its own. " +
        `Give it a drawing, or retire the slot:\n${shared
          .map((slots) => slots.join(" ≡ "))
          .join("\n")}`,
    ).toEqual([]);
  });

  it("keeps host glyphs on the frame's own axes", () => {
    // Transforms can move geometry outside the checked frame.
    expect(
      glyphSource.includes("transform="),
      "no host glyph carries a transform: geometry belongs on the frame's " +
        "grid, and a scale would take the stroke width with it.",
    ).toBe(false);
  });
});
