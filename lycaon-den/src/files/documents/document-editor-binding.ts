import { editorHistory } from "../../components/source/editor/editor-history.ts";
import { Annotation, Prec, Transaction, type ChangeSpec, type Extension } from "@codemirror/state";
import { EditorView, ViewPlugin, keymap } from "@codemirror/view";
import type { DocumentReplica } from "./document-replica.ts";

export const documentSynchronization = Annotation.define<boolean>();
export const documentRemoteChange = Annotation.define<boolean>();

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
  }), Prec.highest(keymap.of([
    { key: "Mod-z", run: command("undo"), preventDefault: true },
    { key: "Mod-Shift-z", run: command("redo"), preventDefault: true },
  ])), EditorView.domEventHandlers({
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
