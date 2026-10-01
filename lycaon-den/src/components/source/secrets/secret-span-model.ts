import type { Text } from "@codemirror/state";
import type { SecretScreen, SecretSpanState } from "../../../api/types.ts";

export type SecretSpanMark = {
  from: number;
  to: number;
  state: SecretSpanState;
  ruleId: string;
  ruleTitle: string;
  reference: string;
  shape: string;
};

/** Incomplete screening state. */
export type SecretScreenGap = "not_screened" | "truncated";

export type SecretScreenSummary = {
  /** Present when coverage is incomplete. */
  gap: SecretScreenGap | null;
  tracked: number;
  retired: number;
  detected: number;
  total: number;
  /** Revision the spans cover, or null when unavailable. */
  screenedRevision: number | null;
  catalogVersion: string;
};

/** Maps host rune offsets to UTF-16 editor positions. */
function runeIndex(doc: Text): (rune: number) => number | null {
  const text = doc.toString();
  const offsets: number[] = [];
  for (let i = 0; i < text.length; ) {
    offsets.push(i);
    const code = text.codePointAt(i);
    i += code !== undefined && code > 0xffff ? 2 : 1;
  }
  offsets.push(text.length);
  return (rune) => {
    if (rune < 0 || rune >= offsets.length) return null;
    return offsets[rune] ?? null;
  };
}

export function secretSpanMarks(
  screen: SecretScreen | null | undefined,
  doc: Text,
): SecretSpanMark[] {
  const spans = screen?.spans;
  if (!spans || spans.length === 0) return [];
  const at = runeIndex(doc);
  const out: SecretSpanMark[] = [];
  for (const span of spans) {
    const from = at(span.start);
    const to = at(span.end);
    if (from === null || to === null || to <= from || to > doc.length) continue;
    out.push({
      from,
      to,
      state: span.state,
      ruleId: span.rule_id,
      ruleTitle: span.rule_title ?? "",
      reference: span.reference ?? "",
      shape: span.shape ?? "",
    });
  }
  out.sort((a, b) => a.from - b.from || a.to - b.to);
  return out;
}

/** Summarizes screening coverage and findings. */
export function secretScreenSummary(
  screen: SecretScreen | null | undefined,
): SecretScreenSummary {
  const empty: SecretScreenSummary = {
    gap: "not_screened",
    tracked: 0,
    retired: 0,
    detected: 0,
    total: 0,
    screenedRevision: null,
    catalogVersion: "",
  };
  if (!screen) return empty;
  let tracked = 0;
  let retired = 0;
  let detected = 0;
  for (const span of screen.spans ?? []) {
    if (span.state === "tracked") tracked += 1;
    else if (span.state === "retired") retired += 1;
    else detected += 1;
  }
  return {
    gap: screen.truncated ? "truncated" : null,
    tracked,
    retired,
    detected,
    total: tracked + retired + detected,
    screenedRevision: screen.screened_revision ?? null,
    catalogVersion: screen.catalog_version ?? "",
  };
}

/** The span covering a position, innermost first. */
export function secretSpanAt(
  marks: readonly SecretSpanMark[],
  pos: number,
): SecretSpanMark | null {
  let best: SecretSpanMark | null = null;
  for (const mark of marks) {
    if (pos < mark.from || pos > mark.to) continue;
    if (!best || mark.to - mark.from < best.to - best.from) best = mark;
  }
  return best;
}

/** Other spans in this buffer whose evidence identity matches. */
export function siblingSecretSpans(
  marks: readonly SecretSpanMark[],
  target: SecretSpanMark,
): SecretSpanMark[] {
  const key = target.reference || `${target.ruleId}\u0000${target.shape}`;
  return marks.filter((mark) => {
    if (mark === target) return false;
    return (mark.reference || `${mark.ruleId}\u0000${mark.shape}`) === key;
  });
}
