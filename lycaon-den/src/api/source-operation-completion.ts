import type { BackendConnection } from "../platform/connection/backend.ts";
import type { SourceOperationEvent, SourceOperationStatus } from "./types.ts";

type Listener = {
  connection: BackendConnection;
  projectId: string;
  operationId: string;
  complete: (status: SourceOperationStatus) => void;
};
const listeners = new Set<Listener>();

/** Observe before admission so a fast completion cannot outrun its HTTP response. */
export function observeSourceOperationCompletion(connection: BackendConnection, projectId: string, operationId: string): {
  result: Promise<SourceOperationStatus>;
  close: () => void;
} {
  let complete!: Listener["complete"];
  const result = new Promise<SourceOperationStatus>((resolve) => { complete = resolve; });
  const listener = { connection: { ...connection }, projectId, operationId, complete };
  listeners.add(listener);
  return { result, close: () => { listeners.delete(listener); } };
}

/** Only the authenticated stream for the admitting host can settle a request. */
export function deliverSourceOperationCompletion(connection: BackendConnection, event: SourceOperationEvent): void {
  if (!event.operation.complete) return;
  for (const listener of listeners) {
    if (listener.projectId !== event.project_id || listener.operationId !== event.operation.operation_id) continue;
    const host = listener.connection;
    if (host.baseUrl !== connection.baseUrl || host.apiToken !== connection.apiToken || host.engineGeneration !== connection.engineGeneration) continue;
    listener.complete(event.operation);
    listeners.delete(listener);
  }
}
