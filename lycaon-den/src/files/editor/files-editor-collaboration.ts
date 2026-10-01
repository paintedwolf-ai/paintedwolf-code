import { Compartment, Facet, StateField, Transaction, type Extension } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import type { DocumentReplica } from "../documents/document-replica.ts";
import { diffSourceText } from "../../components/source/diff/source-text-diff.ts";

const collaboration = new Compartment();
const boundDocument = Facet.define<DocumentReplica["doc"], DocumentReplica["doc"] | undefined>({ combine: (values) => values[0] });
const liveSelection = StateField.define<boolean>({
  create: () => false,
  update: (held, transaction) => held || transaction.selection != null || transaction.docChanged,
});

export function filesCollaborationExtension(replica: DocumentReplica | undefined): Extension {
  return [liveSelection, collaboration.of(replica ? [boundDocument.of(replica.doc), replica.extension] : [])];
}

export function canRestoreFilesCollaborationView(view: EditorView): boolean {
  return !view.state.field(liveSelection);
}

/** Rebinding uses recovered text while preserving live navigation. */
export function rebindFilesCollaboration(view: EditorView, replica: DocumentReplica | undefined): boolean {
  if (!replica) {
    if (!view.state.facet(boundDocument)) return false;
    view.dispatch({ effects: collaboration.reconfigure([]) });
    return true;
  }
  const before = view.state.doc.toString(), text = replica.text.toString();
  if (view.state.facet(boundDocument) === replica.doc && before === text) return false;
  const changes = view.state.changes(diffSourceText(before, text).map(change => ({
    from: change.fromA, to: change.toA, insert: text.slice(change.fromB, change.toB),
  })));
  // Navigation during attachment takes precedence over the saved cursor.
  const selection = view.state.field(liveSelection) ? view.state.selection.map(changes) : replica.history.state.selection;
  view.dispatch({ effects: collaboration.reconfigure([]) });
  view.dispatch({ changes, selection,
    annotations: Transaction.addToHistory.of(false) });
  view.dispatch({ effects: collaboration.reconfigure([boundDocument.of(replica.doc), replica.extension]) });
  return true;
}

export function refreshFilesCollaborationEpoch(view: EditorView, replica: DocumentReplica): void {
  const bound = view.state.facet(boundDocument);
  if (bound && bound !== replica.doc) rebindFilesCollaboration(view, replica);
}
