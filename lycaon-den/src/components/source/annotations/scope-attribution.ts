import type { ChangeDesc, Text } from "@codemirror/state";
import { Decoration, type DecorationSet, type EditorView } from "@codemirror/view";
import type { SourceAttributedText, SourceComparisonAttribution, SourceContributor } from "../../../api/types.ts";
import { sourceContributorLabel } from "./source-contributor-label.ts";
import { wordChanges, type TextRange } from "../diff/word-changes.ts";

function sameContributor(a: SourceContributor, b: SourceContributor): boolean {
  return a.origin === b.origin && a.session_id === b.session_id && a.turn === b.turn
    && a.person_id === b.person_id && a.actor_label === b.actor_label && a.tool_call_id === b.tool_call_id
    && a.tool_name === b.tool_name && a.worker_id === b.worker_id;
}

function sameRuns(a: readonly SourceAttributedText[], b: readonly SourceAttributedText[]): boolean {
  return a === b || (a.length === b.length && a.every((run, i) => {
    const next = b[i]!;
    return run.index === next.index && run.length === next.length && run.selected === next.selected
      && run.visible === next.visible && run.contributors.length === next.contributors.length
      && run.contributors.every((author, index) => sameContributor(author, next.contributors[index]!));
  }));
}

/** Equivalent authorship retains the existing comparison paint. */
export function sameComparisonAttribution(
  a: SourceComparisonAttribution | null | undefined, b: SourceComparisonAttribution | null | undefined,
): boolean {
  return a === b || (!a && !b) || !!(a && b && sameRuns(a.before, b.before) && sameRuns(a.after, b.after));
}

export function attributionLabel(runs: readonly SourceAttributedText[]): string {
  const labels = new Set<string>();
  for (const run of runs) for (const author of run.contributors) labels.add(sourceContributorLabel(author));
  return [...labels].join("; ") || "Authorship unavailable";
}

/** Only unchanged characters retain their saved authorship through live typing. */
export function mapAttribution(runs: readonly SourceAttributedText[], changes: ChangeDesc): SourceAttributedText[] {
  const mapped: SourceAttributedText[] = [];
  changes.iterGaps((from, next, length) => {
    for (const run of runs) {
      if (run.index >= from + length) break;
      const start = Math.max(from, run.index), end = Math.min(from + length, run.index + run.length);
      if (start < end) mapped.push({ ...run, index: next + start - from, length: end - start });
    }
  });
  return mapped;
}

export function runsIn(runs: readonly SourceAttributedText[], from: number, to: number): SourceAttributedText[] {
  return runs.filter((run) => run.visible && run.index < to && run.index + run.length > from);
}

const changedText = Decoration.mark({ class: "cm-changedText" });

/** Selected runs mark their words only where the line also keeps someone else's. */
export function buildAttributedChanges(view: EditorView, runs: readonly SourceAttributedText[]): DecorationSet {
  const marks = [];
  const lines = new Map<number, SourceAttributedText[]>();
  const { doc } = view.state;
  for (const range of view.visibleRanges) {
    const selected: TextRange[] = [];
    for (const run of runs) {
      if (!run.visible) continue;
      const from = Math.max(run.index, range.from);
      const end = Math.min(run.index + run.length, range.to, doc.length);
      if (from >= end) continue;
      const tip = { "data-tip": attributionLabel([run]) };
      marks.push(Decoration.mark(run.selected ? { attributes: tip } : { class: "cm-den-contextText", attributes: tip })
        .range(from, end));
      if (run.selected) selected.push({ from, to: end });
      eachTouchedLine(doc, from, end, (line) => {
        const authors = lines.get(line);
        if (!authors) lines.set(line, [run]);
        else if (!authors.includes(run)) authors.push(run);
      });
    }
    if (selected.length === 0) continue;
    selected.sort((a, b) => a.from - b.from);
    const first = doc.lineAt(selected[0]!.from);
    const last = doc.lineAt(selected[selected.length - 1]!.to - 1);
    const relative = selected.map((piece) => ({ from: piece.from - first.from, to: piece.to - first.from }));
    for (const word of wordChanges(doc.sliceString(first.from, last.to), relative)) {
      marks.push(changedText.range(first.from + word.from, first.from + word.to));
    }
  }
  for (const [from, authors] of lines) {
    marks.push(Decoration.line({ class: authors.some((run) => run.selected) ? "cm-changedLine" : "cm-den-contextLine",
      attributes: { "data-tip": attributionLabel(authors) } }).range(from));
  }
  return Decoration.set(marks, true);
}

function eachTouchedLine(doc: Text, from: number, to: number, visit: (from: number) => void): void {
  let line = doc.lineAt(from);
  for (;;) {
    visit(line.from);
    if (line.to + 1 >= to || line.number === doc.lines) return;
    line = doc.line(line.number + 1);
  }
}
