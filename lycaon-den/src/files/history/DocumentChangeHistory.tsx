import { documentCommand } from "../documents/document-command.ts";
import { For, Show, createSignal, createEffect, on, onCleanup } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { EditorDocumentChange } from "../../api/types.ts";
import { clientIdentity } from "../../platform/connection/client-identity.ts";
import { createPresentationWaiting } from "../../ui/presentation.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { personLabel } from "../../components/source/annotations/source-contributor-label.ts";
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

function changeAuthorLabel(change: EditorDocumentChange): string {
  switch (change.actor_kind) {
    case "agent": return `From ${change.session_title || "agent"}${change.turn ? ` · turn ${change.turn}` : ""}`;
    case "user": return personLabel(change.person_id);
    case "restore": return "Restored content";
  }
}

export function DocumentChangeHistory(props: { client: LycaonClient; projectId: string; documentId: string; onApplied: () => void }) {
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

  return <Show when={changes().length > 0 || waiting()}><div class="flex flex-col gap-2 p-3 text-den-caption" role="group" aria-label="Recent edits">
    <For each={changes()}>{(change) => <div class="flex items-center gap-3">
      <span>{changeAuthorLabel(change)}</span>
      <time dateTime={change.created_at}>{new Date(change.created_at).toLocaleTimeString()}</time>
      <DenButton variant="ghost" compact disabled={busy() || !!change.reverted_by || change.epoch !== editorReplica(props.documentId)?.accepted.epoch}
        onClick={() => void revert(change)}>{change.reverted_by ? "Undone" : "Undo"}</DenButton>
    </div>}</For>
    <Show when={!!cursor()}><DenButton variant="ghost" compact disabled={busy()} onClick={() => void loadEarlier()}>Earlier edits</DenButton></Show>
    <Show when={waiting()}>
      <span class="flex items-center justify-center gap-2 text-den-text-muted" role="status">
        <span class="den-file-version__spinner" aria-hidden="true" />
        <span>Loading edits…</span>
      </span>
    </Show>
  </div></Show>;
}
