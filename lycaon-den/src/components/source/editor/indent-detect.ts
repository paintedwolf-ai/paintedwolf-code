export type IndentStyle = "spaces" | "tabs";

export type IndentInfo = {
  style: IndentStyle;
  /** Columns per indent level (indent_size). */
  width: number;
  /**
   * Display width of a tab character when it differs from `width`
   * (EditorConfig `tab_width`). Defaults to `width` when omitted.
   */
  tabWidth?: number;
};

const WIDTH_CANDIDATES = [2, 4, 8] as const;

/** Empty or tied indentation samples defer to editor preferences. */
export function detectIndent(text: string): IndentInfo | undefined {
  if (!text) return undefined;

  let tabIndented = 0;
  let spaceIndented = 0;
  const spaceWidths: number[] = [];

  for (const raw of text.split(/\r?\n/)) {
    if (!raw || !raw.trim()) continue;
    if (raw.startsWith("\t")) {
      tabIndented++;
      continue;
    }
    let n = 0;
    while (n < raw.length && raw[n] === " ") n++;
    if (n === 0) continue;
    // Mixed leading whitespace on one line is not a clean sample.
    if (raw[n] === "\t") continue;
    spaceIndented++;
    spaceWidths.push(n);
  }

  if (tabIndented === 0 && spaceIndented === 0) return undefined;
  if (tabIndented === spaceIndented) return undefined;

  if (tabIndented > spaceIndented) {
    return { style: "tabs", width: 4 };
  }

  const width = inferSpaceWidth(spaceWidths);
  if (width == null) return undefined;
  return { style: "spaces", width };
}

function inferSpaceWidth(widths: readonly number[]): number | undefined {
  if (widths.length === 0) return undefined;
  const scores = new Map<number, number>();
  for (const w of WIDTH_CANDIDATES) scores.set(w, 0);

  for (const n of widths) {
    for (const w of WIDTH_CANDIDATES) {
      if (n % w === 0) {
        scores.set(w, (scores.get(w) ?? 0) + 1);
      }
    }
  }

  let best: number | undefined;
  let bestScore = 0;
  let tied = false;
  for (const w of WIDTH_CANDIDATES) {
    const s = scores.get(w) ?? 0;
    if (s > bestScore) {
      best = w;
      bestScore = s;
      tied = false;
    } else if (s === bestScore && s > 0) {
      tied = true;
    }
  }
  if (best == null || bestScore === 0 || tied) {
    // Prefer the smallest common prefix length among {2,4,8} when every
    // sample shares one exact width.
    const exact = new Map<number, number>();
    for (const n of widths) {
      if ((WIDTH_CANDIDATES as readonly number[]).includes(n)) {
        exact.set(n, (exact.get(n) ?? 0) + 1);
      }
    }
    let exactBest: number | undefined;
    let exactScore = 0;
    for (const [w, s] of exact) {
      if (s > exactScore) {
        exactBest = w;
        exactScore = s;
      }
    }
    return exactBest;
  }
  return best;
}

/** Indentation unit for an IndentInfo. */
export function indentUnitString(info: IndentInfo): string {
  if (info.style === "spaces") return " ".repeat(info.width);
  const tabWidth = info.tabWidth ?? info.width;
  if (tabWidth <= 0 || info.width <= 0) return "\t";
  // Fill one indent with tabs, then spaces.
  const tabs = Math.floor(info.width / tabWidth);
  const spaces = info.width % tabWidth;
  if (tabs === 0 && spaces === 0) return "\t";
  return "\t".repeat(Math.max(tabs, 0)) + " ".repeat(Math.max(spaces, 0));
}

export function tabSizeForIndent(info: IndentInfo): number {
  return info.tabWidth ?? info.width;
}

export function indentChipLabel(info: IndentInfo): string {
  if (info.style === "tabs") {
    const tw = info.tabWidth ?? info.width;
    return tw === info.width ? "Tabs" : `Tabs: ${info.width} (tw ${tw})`;
  }
  return `Spaces: ${info.width}`;
}
