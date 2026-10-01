import { Decoration, ViewPlugin, type DecorationSet, type EditorView, type ViewUpdate } from "@codemirror/view";
import { StateEffect } from "@codemirror/state";
import { documentRemoteChange } from "./document-editor-binding.ts";

const clearHighlights = StateEffect.define<void>();

/** Remote changes settle in the binding transaction. Only their line tint expires. */
export const documentChangeHighlights = ViewPlugin.fromClass(class {
  decorations: DecorationSet = Decoration.none;
  private timer: ReturnType<typeof setTimeout> | undefined;
  constructor(private readonly view: EditorView) {}

  update(update: ViewUpdate): void {
    this.decorations = this.decorations.map(update.changes);
    if (update.transactions.some((transaction) => transaction.effects.some((effect) => effect.is(clearHighlights)))) {
      this.decorations = Decoration.none;
    }
    if (!update.docChanged || !update.transactions.some((transaction) => transaction.annotation(documentRemoteChange))) return;
    const lines = new Map<number, string>();
    update.changes.iterChangedRanges((_fromA, _toA, fromB, toB) => {
      for (const visible of update.view.visibleRanges) {
        const from = Math.max(fromB, visible.from), to = Math.min(toB, visible.to);
        if (from > to) continue;
        const first = update.state.doc.lineAt(from).number;
        const last = update.state.doc.lineAt(to).number;
        for (let line = first; line <= last && lines.size < 128; line++) {
          lines.set(update.state.doc.line(line).from, fromB === toB ? "cm-den-hunk-del" : "cm-den-hunk-add");
        }
      }
    });
    this.decorations = Decoration.set([...lines].map(([from, className]) => Decoration.line({ class: className }).range(from)), true);
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.view.dispatch({ effects: clearHighlights.of(undefined) }), 800);
  }

  destroy(): void { clearTimeout(this.timer); }
}, { decorations: (plugin) => plugin.decorations });
