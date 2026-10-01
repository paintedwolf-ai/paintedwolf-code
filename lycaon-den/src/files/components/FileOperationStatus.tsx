import { For, Show, createEffect, createSignal, onCleanup, untrack } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { SourceOperationStoppedError } from "../../api/source-operation.ts";
import type { SourceOperationStatus } from "../../api/types.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import { fileOperationDetail, fileOperationLabel, fileOperationRetryable } from "../../store/file-operations.ts";
import { replaceSourceOperations, sourceOperationsEventRevision, sourceOperationsFor, sourceOperationsResyncRevision } from "../../store/source-operations.ts";
import { useNow } from "../../time/now.ts";
import { DenButton } from "../../components/primitives/DenButton.tsx";

/** Completed operations stay visible this long after the host's last update. */
const COMPLETED_VISIBLE_MS = 8_000;

type Props = {
  projectId: string;
  client: LycaonClient | null;
  onRetry: (operation: SourceOperationStatus) => Promise<void>;
  onComplete: (operation: SourceOperationStatus) => void;
};

/** Lists once per event stream; `source_operation` events carry later changes. */
export function FileOperationStatus(props: Props) {
  const [dismissed, setDismissed] = createSignal<Set<string>>(new Set());
  const [connectionError, setConnectionError] = createSignal<string | null>(null);
  const [actionError, setActionError] = createSignal<string | null>(null);
  const error = () => actionError() ?? connectionError();
  const [retrying, setRetrying] = createSignal<Set<string>>(new Set());
  const [cancelling, setCancelling] = createSignal<Set<string>>(new Set());
  const requests = () => sourceOperationsFor(props.projectId);
  createEffect(() => {
    const client = props.client;
    const projectId = props.projectId;
    sourceOperationsResyncRevision();
    setDismissed(new Set<string>());
    setConnectionError(null);
    setActionError(null);
    if (!client) return;
    let disposed = false;
    const snapshotRevision = sourceOperationsEventRevision();
    void client.listSourceOperations(projectId).then((result) => {
      if (disposed) return;
      replaceSourceOperations(projectId, result.operations, snapshotRevision);
      setConnectionError(null);
    }).catch((error: unknown) => {
      if (!disposed) setConnectionError(error instanceof BackendTransportError
        ? "Connection interrupted. File operations continue on the host."
        : error instanceof Error ? error.message : "Could not read file operation status.");
    });
    onCleanup(() => { disposed = true; });
  });
  // A completion counts once the operation was seen in an earlier state.
  const observed = new Map<string, string>();
  createEffect(() => {
    const completed: SourceOperationStatus[] = [];
    for (const entry of requests()) {
      const previous = observed.get(entry.operation_id);
      if (previous !== undefined && previous !== entry.state && entry.state === "completed") completed.push(entry);
    }
    observed.clear();
    for (const entry of requests()) observed.set(entry.operation_id, entry.state);
    for (const entry of completed) untrack(() => props.onComplete(entry));
  });
  const hasCompleted = () => requests().some((entry) => entry.state === "completed" && !dismissed().has(entry.operation_id));
  const now = useNow("second", hasCompleted);
  const visible = () => requests().filter((entry) => {
    if (dismissed().has(entry.operation_id)) return false;
    if (entry.state === "completed") return entry.completed_at !== undefined && now() - Date.parse(entry.completed_at) < COMPLETED_VISIBLE_MS;
    return true;
  });
  const act = async (entry: SourceOperationStatus, cancel: boolean) => {
    const client = props.client;
    if (!client || cancelling().has(entry.operation_id) || (!cancel && retrying().has(entry.operation_id))) return;
    const setPending = cancel ? setCancelling : setRetrying;
    setPending((held) => new Set([...held, entry.operation_id]));
    setActionError(null);
    try {
      if (cancel) await client.cancelSourceOperation(props.projectId, entry.operation_id);
      else await props.onRetry(entry);
    } catch (error) {
      if (!(error instanceof SourceOperationStoppedError && error.state === "canceled")) {
        setActionError(error instanceof Error ? error.message : "Could not update the file operation.");
      }
    } finally {
      setPending((held) => {
        const next = new Set(held);
        next.delete(entry.operation_id);
        return next;
      });
    }
  };
  return (
    <Show when={visible().length > 0}>
      <section class="den-file-operations" aria-label="File operations" data-testid="file-operations">
        <Show when={error()}><p role="status">{error()}</p></Show>
        <For each={visible()}>{(entry) => (
          <div class="den-file-operations__entry" data-state={entry.state}>
            <div class="den-file-operations__description" role="status" aria-live="polite">
              <strong>{fileOperationLabel(entry)}{entry.path ? `: ${entry.path}` : ""}</strong>
              <span>{fileOperationDetail(entry)}</span>
            </div>
            <Show when={entry.cancelable && !entry.complete}>
              <DenButton compact variant="ghost" disabled={cancelling().has(entry.operation_id)} onClick={() => void act(entry, true)}>Cancel</DenButton>
            </Show>
            <Show when={fileOperationRetryable(entry)}>
              <DenButton compact variant="ghost" disabled={retrying().has(entry.operation_id) || cancelling().has(entry.operation_id)} onClick={() => void act(entry, false)}>Retry</DenButton>
            </Show>
            <Show when={entry.complete}>
              <DenButton compact variant="ghost" aria-label={`Dismiss ${fileOperationLabel(entry).toLowerCase()}`} onClick={() => setDismissed((held) => new Set([...held, entry.operation_id]))}>Dismiss</DenButton>
            </Show>
          </div>
        )}</For>
      </section>
    </Show>
  );
}
