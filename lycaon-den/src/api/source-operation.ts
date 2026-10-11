import type { BackendConnection } from "../platform/connection/backend.ts";
import { BackendTransportError } from "../platform/connection/request-connectivity.ts";
import { LycaonApiError, lycaonFetch, lycaonJson, requireSuccessfulResponse } from "./http.ts";
import { observeSourceOperationCompletion } from "./source-operation-completion.ts";
import type { SourceOperationStatus } from "./types.ts";

export class SourceOperationStoppedError extends Error {
  constructor(readonly state: "canceled" | "interrupted", readonly operationId: string) {
    super(state === "canceled" ? "File operation canceled." : "The file operation was interrupted. Review its status and retry when ready.");
    this.name = "SourceOperationStoppedError";
  }
}

async function sourceResponse<T>(response: Response): Promise<T> {
  await requireSuccessfulResponse(response);
  return response.status === 204 ? undefined as T : await response.json() as T;
}

export async function sourceOperationResult<T>(state: SourceOperationStatus): Promise<T> {
  if (state.state === "canceled" || state.state === "interrupted") {
    throw new SourceOperationStoppedError(state.state, state.operation_id);
  }
  if (!state.complete) throw new Error("The file operation has no recorded result.");
  if (state.error) {
    throw new LycaonApiError(
      state.error.message,
      undefined,
      state.error.code,
      {
        title: state.error.title,
        retryable: state.error.retryable,
        suggestedAction: state.error.suggested_action,
        actions: state.error.actions,
        tier: state.error.tier,
        scope: state.error.scope,
        resolution: state.error.resolution,
        details: state.error.details,
      },
    );
  }
  return state.result as T;
}

/** Reconnection reads the accepted receipt using the original mutation identity. */
async function followSourceOperation<T>(connection: BackendConnection, projectId: string, id: string, signal: AbortSignal): Promise<T> {
  let delay = 750;
  for (;;) {
    signal.throwIfAborted();
    await new Promise<void>((resolve, reject) => {
      const abort = () => { clearTimeout(timer); reject(signal.reason instanceof Error ? signal.reason : new DOMException("The operation was aborted.", "AbortError")); };
      const timer = globalThis.setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, delay);
      signal.addEventListener("abort", abort, { once: true });
      if (signal.aborted) abort();
    });
    let state: SourceOperationStatus;
    try {
      state = await lycaonJson<SourceOperationStatus>(connection,
        `/v1/projects/${encodeURIComponent(projectId)}/source/operations/${encodeURIComponent(id)}`, { signal });
      delay = 750;
    } catch (error) {
      if (!(error instanceof BackendTransportError)) throw error;
      delay = Math.min(delay * 2, 15000);
      continue;
    }
    if (state.complete) return sourceOperationResult<T>(state);
  }
}

async function submitSourceOperation<T>(connection: BackendConnection, projectId: string, id: string, path: string, init: RequestInit, signal: AbortSignal): Promise<T> {
  try {
    const response = await lycaonFetch(connection, path, { ...init, signal });
    if (response.status !== 202) return await sourceResponse<T>(response);
    const state = await response.json() as SourceOperationStatus;
    if (state.complete) return await sourceOperationResult<T>(state);
  } catch (error) {
    // A body read can fail after the host has already completed the mutation.
    if (error instanceof LycaonApiError || error instanceof SourceOperationStoppedError) throw error;
    if (!(error instanceof BackendTransportError) && !(error instanceof TypeError)) throw error;
  }
  return followSourceOperation<T>(connection, projectId, id, signal);
}

export async function sourceOperation<T>(connection: BackendConnection, projectId: string, id: string, path: string, init: RequestInit): Promise<T> {
  const completion = observeSourceOperationCompletion(connection, projectId, id);
  const follower = new AbortController();
  const signal = init.signal ? AbortSignal.any([init.signal, follower.signal]) : follower.signal;
  try {
    return await Promise.race([
      submitSourceOperation<T>(connection, projectId, id, path, init, signal),
      completion.result.then(sourceOperationResult<T>),
    ]);
  } finally {
    completion.close();
    follower.abort();
  }
}
