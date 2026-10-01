import { history, historyField, isolateHistory, redo, undo, undoDepth, redoDepth } from "@codemirror/commands";
import { EditorState, StateField, Transaction, type TransactionSpec } from "@codemirror/state";
import { editorCspNonce } from "../../components/source/editor/editor-csp-nonce.ts";

const fields = { history: historyField };
const MAX_HISTORY_BYTES = 16 * 1024 * 1024;
type SavedHistory = { selection: unknown; history: { done: unknown[]; undone: unknown[] } };
const lastEditEvent = StateField.define<string | undefined>({
  create: () => undefined,
  update: (previous, transaction) => transaction.docChanged && transaction.annotation(Transaction.addToHistory) !== false
    ? transaction.annotation(Transaction.userEvent) : previous,
});
function groupsTyping(event: string | undefined): boolean {
  return event === "input.type" || event === "input.type.compose" || event === "input.type.compose.start"
    || event === "delete.backward" || event === "delete.forward";
}
const extensions = () => [editorCspNonce(), lastEditEvent, history({ minDepth: 200,
  joinToEvent: (transaction, adjacent) => adjacent
    && groupsTyping(transaction.annotation(Transaction.userEvent))
    && transaction.annotation(Transaction.userEvent) === transaction.startState.field(lastEditEvent),
}), EditorState.allowMultipleSelections.of(true)];

/** The window replica retains document history independently of mounted views. */
export class DocumentHistory {
  private current = EditorState.create({ extensions: extensions() });
  /** Advances with every text change or history reset; selection moves leave it alone. */
  revision = 0;
  private retainedBytes = 0;
  get estimatedBytes(): number { return this.retainedBytes + this.current.doc.length * 2; }

  get hasHistory(): boolean { return undoDepth(this.current) > 0 || redoDepth(this.current) > 0; }

  get state(): EditorState { return this.current; }
  set state(next: EditorState) {
    if (next.doc !== this.current.doc) this.revision++;
    this.current = next;
  }

  /** A preserved snapshot carries selection and history only; the replica text is its document. */
  reset(text: string, saved?: unknown): void {
    this.current = saved
      ? EditorState.fromJSON({ ...(saved as object), doc: text }, { extensions: extensions() }, fields)
      : EditorState.create({ doc: text, extensions: extensions() });
    this.revision++;
  }

  snapshot(): unknown {
    const json = this.state.toJSON(fields) as SavedHistory & { doc: string };
    const { doc: _doc, ...saved } = json;
    const branches = saved.history;
    let bytes = new TextEncoder().encode(JSON.stringify(branches)).length;
    let trimmed = false;
    while (bytes > MAX_HISTORY_BYTES && branches.done.length + branches.undone.length > 1) {
      if (branches.done.length > 1 || !branches.undone.length) branches.done.shift();
      else branches.undone.shift();
      bytes = new TextEncoder().encode(JSON.stringify(branches)).length;
      trimmed = true;
    }
    this.retainedBytes = bytes;
    if (trimmed) this.current = EditorState.fromJSON(json, { extensions: extensions() }, fields);
    return saved;
  }

  apply(spec: TransactionSpec): Transaction {
    const transaction = this.state.update(spec);
    this.state = transaction.state;
    return transaction;
  }

  boundary(): void {
    this.apply({ annotations: [isolateHistory.of("full"), Transaction.addToHistory.of(false)] });
  }

  step(direction: "undo" | "redo", accept: (transaction: Transaction) => void): boolean {
    return (direction === "undo" ? undo : redo)({ state: this.state, dispatch: accept });
  }
}
