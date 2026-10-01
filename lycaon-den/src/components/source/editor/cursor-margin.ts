import { Annotation, type Transaction } from "@codemirror/state";
import { EditorView, ViewPlugin, type ViewUpdate } from "@codemirror/view";

/** Lines kept visible above and below the caret after keyboard movement. */
export const CURSOR_MARGIN_LINES = 3;

const cursorMarginReveal = Annotation.define<boolean>();

/** Keyboard and API moves ask to reveal; pointer selection does not. */
function revealsByKeyboard(tr: Transaction): boolean {
  return (
    tr.scrollIntoView &&
    !tr.isUserEvent("select.pointer") &&
    tr.annotation(cursorMarginReveal) !== true
  );
}

/** Widens the reveal that keyboard movement already requests to a line margin. */
export function cursorMargin(lines = CURSOR_MARGIN_LINES) {
  return ViewPlugin.fromClass(
    class {
      update(update: ViewUpdate) {
        if (!update.transactions.some(revealsByKeyboard)) return;
        const view = update.view;
        const head = update.state.selection.main.head;
        // The update is still in progress; the reveal lands before its frame.
        queueMicrotask(() => {
          if (!view.dom.isConnected || view.state.selection.main.head !== head) {
            return;
          }
          view.dispatch({
            effects: EditorView.scrollIntoView(head, {
              y: "nearest",
              yMargin: lines * view.defaultLineHeight,
            }),
            annotations: cursorMarginReveal.of(true),
          });
        });
      }
    },
  );
}
