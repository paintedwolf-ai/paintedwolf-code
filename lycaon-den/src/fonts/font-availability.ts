/** Availability of an unbundled font family. */
export type FontAvailability = "available" | "unavailable" | "unknown";

/** Multiple sentinels avoid a coincidental metric match. */
const SENTINELS = ["monospace", "serif", "sans-serif"] as const;

/** Mixed glyph widths distinguish most faces. */
const PROBE_TEXT = "mmmmmmmmmmlliWWWWWW0Oo1Il";

const PROBE_SIZE = "72px";

function probeContext(): CanvasRenderingContext2D | null {
  try {
    return document.createElement("canvas").getContext("2d");
  } catch {
    return null;
  }
}

function widthOf(context: CanvasRenderingContext2D, font: string): number {
  context.font = font;
  return context.measureText(PROBE_TEXT).width;
}

/** Compares glyph widths with fallback sentinels. */
export function probeFontAvailability(family: string): FontAvailability {
  const name = family.trim();
  if (!name) return "unknown";

  const context = probeContext();
  if (!context) return "unknown";

  const quoted = `"${name.replace(/"/g, '\\"')}"`;
  for (const sentinel of SENTINELS) {
    const baseline = widthOf(context, `${PROBE_SIZE} ${sentinel}`);
    const candidate = widthOf(context, `${PROBE_SIZE} ${quoted}, ${sentinel}`);
    if (baseline <= 0) return "unknown";
    if (candidate !== baseline) return "available";
  }
  return "unavailable";
}
