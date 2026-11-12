import { MapMode, StateEffect, StateField, type EditorState, type Transaction } from "@codemirror/state";
import { Decoration, EditorView, ViewPlugin, type ViewUpdate } from "@codemirror/view";
import { AGENT_LINGER_MS } from "../components/agent-presence.ts";
import { documentRemoteChange } from "./document-editor-binding.ts";

/** How long a landed line takes to fade once its linger time is over. */
export const LANDED_LINE_FADE_MS = 600;
/** Lines recorded per document; the oldest go first. */
export const LANDED_LINES_MAX = 512;

export type LandedLine = {
  /** Start of the line in the current document. */
  from: number;
  kind: "add" | "del";
  /** When a change from elsewhere last landed on the line. */
  at: number;
};

const expireLandedLines = StateEffect.define<number>();

function mapLines(lines: readonly LandedLine[], transaction: Transaction): LandedLine[] {
  const { doc } = transaction.state;
  const byLine = new Map<number, LandedLine>();
  for (const line of lines) {
    // A line whose start was deleted no longer exists.
    const mapped = transaction.changes.mapPos(line.from, 1, MapMode.TrackDel);
    if (mapped === null) continue;
    const from = doc.lineAt(mapped).from;
    const held = byLine.get(from);
    if (!held || held.at < line.at) byLine.set(from, { ...line, from });
  }
  return [...byLine.values()];
}

/** Lines in the new document that a transaction changed, at most `limit`. */
function changedLines(transaction: Transaction, limit: number): Array<{ from: number; kind: LandedLine["kind"] }> {
  const { doc } = transaction.state;
  const out = new Map<number, LandedLine["kind"]>();
  transaction.changes.iterChangedRanges((_fromA, _toA, fromB, toB) => {
    const kind = fromB === toB ? "del" : "add";
    const last = doc.lineAt(toB).number;
    for (let number = doc.lineAt(fromB).number; number <= last && out.size < limit; number++) {
      out.set(doc.line(number).from, kind);
    }
  });
  return [...out].map(([from, kind]) => ({ from, kind }));
}

function withoutTouched(lines: readonly LandedLine[], transaction: Transaction): LandedLine[] {
  const touched = new Set(changedLines(transaction, Number.POSITIVE_INFINITY).map((line) => line.from));
  return lines.filter((line) => !touched.has(line.from));
}

function newestFirst(lines: LandedLine[]): LandedLine[] {
  return lines.sort((a, b) => b.at - a.at || a.from - b.from).slice(0, LANDED_LINES_MAX);
}

/** Lines changes from elsewhere landed on, each kept for the agent linger time after its own last change. */
export const landedLinesField = StateField.define<readonly LandedLine[]>({
  create: () => [],
  update(lines, transaction) {
    let next = lines;
    if (transaction.docChanged) {
      next = mapLines(next, transaction);
      if (transaction.annotation(documentRemoteChange)) {
        const at = Date.now();
        const landed = changedLines(transaction, LANDED_LINES_MAX).map((line) => ({ ...line, at }));
        const fresh = new Set(landed.map((line) => line.from));
        next = newestFirst([...landed, ...next.filter((line) => !fresh.has(line.from))]);
      } else {
        // The person's own edit is now the newest thing on the line.
        next = withoutTouched(next, transaction);
      }
    }
    for (const effect of transaction.effects) {
      if (effect.is(expireLandedLines)) next = next.filter((line) => effect.value - line.at < AGENT_LINGER_MS + LANDED_LINE_FADE_MS);
    }
    return next;
  },
  provide: (field) => EditorView.decorations.compute([field], (state) => {
    const now = Date.now();
    const lines = [...state.field(field)].sort((a, b) => a.from - b.from);
    return Decoration.set(lines.map((line) => Decoration.line({
      class: `cm-den-hunk-${line.kind}${now - line.at >= AGENT_LINGER_MS ? " cm-den-hunk--leaving" : ""}`,
    }).range(line.from)));
  }),
});

export function landedLines(state: EditorState): readonly LandedLine[] {
  return state.field(landedLinesField, false) ?? [];
}

/** Wakes once for the next line that starts leaving or goes. */
const landedLineExpiry = ViewPlugin.fromClass(class {
  private timer: ReturnType<typeof setTimeout> | undefined;
  constructor(private readonly view: EditorView) {
    this.schedule();
  }

  update(update: ViewUpdate): void {
    if (landedLines(update.startState) !== landedLines(update.state)) this.schedule();
  }

  private schedule(): void {
    clearTimeout(this.timer);
    this.timer = undefined;
    const now = Date.now();
    let due = Number.POSITIVE_INFINITY;
    for (const line of landedLines(this.view.state)) {
      const leaving = line.at + AGENT_LINGER_MS;
      due = Math.min(due, leaving > now ? leaving : leaving + LANDED_LINE_FADE_MS);
    }
    if (!Number.isFinite(due)) return;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      this.view.dispatch({ effects: expireLandedLines.of(Date.now()) });
    }, Math.max(0, due - now));
  }

  destroy(): void {
    clearTimeout(this.timer);
  }
});

export const documentChangeHighlights = [landedLinesField, landedLineExpiry];
