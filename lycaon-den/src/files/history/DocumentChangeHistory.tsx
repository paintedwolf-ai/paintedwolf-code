import { documentCommand } from "../documents/document-command.ts";
import { For, Show, createSignal, createEffect, on, onCleanup, createMemo, type JSX } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { EditorDocumentChange } from "../../api/types.ts";
import { clientIdentity } from "../../platform/connection/client-identity.ts";
import { createPresentationWaiting } from "../../ui/presentation.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { documentHistoryTimeline } from "./document-history-timeline.ts";
import type { FileHistoryRow } from "./file-history-model.ts";
import { DocumentEditGroup } from "./DocumentEditGroup.tsx";
import { editorReplica } from "../documents/editor-document.ts";
import { observeSurfaceFailure, reportSurfaceFailure } from "../../notices/surface-failure.ts";

const historyFailure = {
  code: "document_change_history_unavailable",
  title: "Recent edits unavailable",
  suggestedAction: "Reopen the document to load its recent edits again.",
};
const undoFailure = {
  code: "document_edit_undo_failed",
  title: "Couldn’t undo the edit",
  suggestedAction: "Check that the document is still open and connected, then undo the edit again.",
};

export type DocumentHistorySource = {
  client: LycaonClient;
  projectId: string;
  documentId: string;
  onApplied: () => void;
};

export function DocumentChangeHistory(props: DocumentHistorySource & {
  rows: readonly FileHistoryRow[];
  renderRow: (row: FileHistoryRow) => JSX.Element;
  emptyMessage?: JSX.Element;
}) {
  const history = createSurfaceQuery({
    name: "document-change-history",
    required: false,
    source: () => ({ client: props.client, key: JSON.stringify([props.projectId, props.documentId]), projectId: props.projectId, documentId: props.documentId }),
    load: ({ client, projectId, documentId }) => client.listEditorDocumentChanges(projectId, documentId),
  });
  const changes = () => history.value()?.changes ?? [];
  const cursor = () => history.value()?.next_cursor;
  const [working, setWorking] = createSignal(false);
  const busy = () => working() || history.loading();
  observeSurfaceFailure(historyFailure, history.error, () => props.projectId);
  const actionWaiting = createPresentationWaiting(working);
  const waiting = () => history.showLoading() || actionWaiting();
  let generation = 0;
  onCleanup(() => generation++);

  const loadEarlier = async () => {
    if (busy()) return;
    const current = ++generation;
    const target = history.capture();
    setWorking(true);
    try {
      const response = await props.client.listEditorDocumentChanges(props.projectId, props.documentId, cursor());
      if (generation !== current) return;
      target.update((previous) => ({ ...response, changes: [...(previous?.changes ?? []), ...response.changes] }));
    } catch (cause) {
      if (generation === current) reportSurfaceFailure(historyFailure, cause, props.projectId);
    } finally { if (generation === current) setWorking(false); }
  };

  const revert = async (change: EditorDocumentChange) => {
    const replica = editorReplica(props.documentId);
    if (!replica || busy()) return;
    const current = ++generation;
    setWorking(true);
    try {
      await replica.flush();
      await replica.executeCommand(documentCommand(replica.accepted, { action: "revert", changeId: change.operation_id, request: {
        ...replica.authorship,
        client_id: clientIdentity(), operation_id: crypto.randomUUID(), epoch: replica.accepted.epoch,
      } }));
      if (generation === current) props.onApplied();
    } catch (cause) { if (generation === current) reportSurfaceFailure(undoFailure, cause, props.projectId); }
    finally { if (generation === current) setWorking(false); }
  };

  createEffect(on(() => [props.client, props.projectId, props.documentId] as const, () => {
    generation++;
    setWorking(false);
  }));

  const rows = createMemo(() => documentHistoryTimeline(props.rows, changes()));
  return <>
    <Show when={!history.loading() && rows().length === 0}>{props.emptyMessage}</Show>
    <For each={rows()}>{(row) => row.kind === "edits" ? (
      <DocumentEditGroup
        changes={row.changes}
        related={row.related}
        canUndo={(change) => !busy() && !change.reverted_by &&
          change.epoch === editorReplica(props.documentId)?.accepted.epoch}
        onUndo={(change) => void revert(change)}
      />
    ) : props.renderRow(row)}</For>
    <Show when={!!cursor()}>
      <DenButton data-testid="earlier-edits" variant="ghost" compact disabled={busy()} onClick={() => void loadEarlier()}>
        Load earlier document edits
      </DenButton>
    </Show>
    <Show when={waiting()}>
      <div class="den-file-version__git-loading" role="status">
        <span class="den-file-version__spinner" aria-hidden="true" />
        <span>Loading edits…</span>
      </div>
    </Show>
  </>;
}
