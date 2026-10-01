import type { BackendConnection } from "../platform/connection/backend.ts";
import type { LycaonClient } from "./client.ts";
import { lycaonJson } from "./http.ts";
import { createSourceViewsClient } from "./source-views-client.ts";
import { createHostClient } from "./http-capabilities/host.ts";
import { createSessionsClient } from "./http-capabilities/sessions.ts";
import { createWorkflowsClient } from "./http-capabilities/workflows.ts";
import { createProjectsClient } from "./http-capabilities/projects.ts";
import { createSourceClient } from "./http-capabilities/source.ts";
import { createSourceHistoryClient } from "./http-capabilities/source-history.ts";
import { createDocumentsClient } from "./http-capabilities/documents.ts";
import { createSearchClient } from "./http-capabilities/search.ts";
import { createGitClient } from "./http-capabilities/git.ts";
import { createSecurityClient } from "./http-capabilities/security.ts";
import { createSettingsClient } from "./http-capabilities/settings.ts";
import { createExtensionsClient } from "./http-capabilities/extensions.ts";
import { createMcpClient } from "./http-capabilities/mcp.ts";
import { createStorageClient } from "./http-capabilities/storage.ts";

function abortReason(signal: AbortSignal): Error {
  return signal.reason instanceof Error
    ? signal.reason
    : new DOMException("The operation was aborted.", "AbortError");
}

function raceAbort<T>(shared: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) return Promise.reject(abortReason(signal));
  return new Promise<T>((resolve, reject) => {
    const onAbort = () => reject(abortReason(signal));
    signal.addEventListener("abort", onAbort, { once: true });
    shared.then(resolve, reject).finally(() => signal.removeEventListener("abort", onAbort));
  });
}

/** Creates an HTTP client that attaches the connection bearer. */
export function createLycaonClient(connection: BackendConnection): LycaonClient {
  const getFlights = new Map<string, Promise<unknown>>();
  const j = <T>(path: string, init?: RequestInit): Promise<T> => {
    const method = (init?.method ?? "GET").toUpperCase();
    if (method !== "GET") return lycaonJson<T>(connection, path, init);
    const signal = init?.signal;
    if (signal) {
      // Cancellation affects this caller, not the shared read.
      const shared = getFlights.get(path);
      return shared
        ? raceAbort(shared as Promise<T>, signal)
        : lycaonJson<T>(connection, path, init);
    }
    const existing = getFlights.get(path);
    if (existing) return existing as Promise<T>;
    const flight = lycaonJson<T>(connection, path, init).finally(() => {
      if (getFlights.get(path) === flight) getFlights.delete(path);
    });
    getFlights.set(path, flight);
    return flight;
  };
  return {
    ...createSourceViewsClient(j),
    ...createHostClient(j),
    ...createSessionsClient(j, connection),
    ...createWorkflowsClient(j, connection),
    ...createProjectsClient(j),
    ...createSourceClient(j, connection),
    ...createSourceHistoryClient(j),
    ...createDocumentsClient(j),
    ...createSearchClient(j, connection),
    ...createGitClient(j),
    ...createSecurityClient(j, connection),
    ...createSettingsClient(j),
    ...createExtensionsClient(j, connection),
    ...createMcpClient(j),
    ...createStorageClient(j, connection),
  } satisfies LycaonClient;
}
