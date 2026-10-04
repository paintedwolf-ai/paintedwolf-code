import { editorHistory } from "../../components/source/editor/editor-history.ts";
import { Annotation, Prec, Transaction, type ChangeSpec, type Extension } from "@codemirror/state";
import { EditorView, ViewPlugin, keymap } from "@codemirror/view";
import type { DocumentReplica } from "./document-replica.ts";
import { modPressed, shortcutPlatform } from "../../shortcuts/platform.ts";
import type { TauriPlatform } from "../../platform/runtime.ts";

export const documentSynchronization = Annotation.define<boolean>();
export const documentRemoteChange = Annotation.define<boolean>();

type HistoryKeyEvent = Pick<KeyboardEvent, "key" | "code" | "shiftKey" | "altKey" | "ctrlKey" | "metaKey">;

/**
 * The primary modifier with Z undoes, and with Shift added redoes. Shift is read from the event, not
 * the key text: a browser may report Control+Shift+Z with the unshifted "z", which a keymap matches as undo.
 */
export function documentHistoryKey(event: HistoryKeyEvent, platform: TauriPlatform = shortcutPlatform()): "undo" | "redo" | null {
  const other = platform === "macos" ? event.ctrlKey : event.metaKey;
  if (!modPressed(event, platform) || event.altKey || other) return null;
  const z = /^[a-z]$/i.test(event.key) ? event.key.toLowerCase() === "z" : event.code === "KeyZ";
  return z ? event.shiftKey ? "redo" : "undo" : null;
}

export function documentEditorBinding(replica: DocumentReplica): Extension {
  const command = (direction: "undo" | "redo") => () => { replica.stepHistory(direction); return true; };
  return [editorHistory.of(direction => replica.stepHistory(direction)), ViewPlugin.define((view) => {
    replica.attachEditor(view);
    return {
      update(update) {
        for (const transaction of update.transactions) {
          if (!transaction.annotation(documentSynchronization)) replica.acceptEditorTransaction(transaction);
        }
      },
      destroy() { replica.detachEditor(view); },
    };
  }), Prec.highest(EditorView.domEventHandlers({
    // Ahead of every keymap, including the stock undo bindings an editable view carries.
    keydown(event) {
      const direction = event.isComposing ? null : documentHistoryKey(event);
      if (!direction) return false;
      event.preventDefault();
      return command(direction)();
    },
  })), Prec.highest(keymap.of([{
    // Keymaps run after editing styles, so Emacs keeps Control+Y for yank and Vim for scrolling.
    key: "Mod-y",
    run: () => shortcutPlatform() !== "macos" && command("redo")(),
  }])), EditorView.domEventHandlers({
    beforeinput(event) {
      if (event.inputType === "historyUndo") return command("undo")();
      if (event.inputType === "historyRedo") return command("redo")();
      return false;
    },
    blur() { replica.history.boundary(); return false; },
  })];
}

export function documentDeltaChanges(delta: { insert?: unknown; delete?: number; retain?: number }[]): ChangeSpec[] {
  let position = 0;
  const changes: ChangeSpec[] = [];
  for (const part of delta) {
    if (typeof part.insert === "string") changes.push({ from: position, insert: part.insert });
    else if (part.delete) { changes.push({ from: position, to: position + part.delete }); position += part.delete; }
    else position += part.retain ?? 0;
  }
  return changes;
}

export function synchronizeEditor(view: EditorView | undefined, transaction: Transaction, remote = false): void {
  view?.dispatch({ changes: transaction.changes, selection: transaction.state.selection,
    annotations: [documentSynchronization.of(true), documentRemoteChange.of(remote), Transaction.addToHistory.of(false)],
    userEvent: remote ? undefined : transaction.annotation(Transaction.userEvent),
    scrollIntoView: transaction.scrollIntoView });
}
