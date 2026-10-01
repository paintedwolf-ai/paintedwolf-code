import { LycaonApiError } from "../../api/http.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import { workspaceMismatchId } from "./source-workspace-identity.ts";

export const WORKSPACE_SYNC_RETRYING = "Editor sync is unavailable — retrying…";

export const WORKSPACE_FAULT_GENERIC = "The editor workspace could not be opened.";

export const WORKSPACE_FAULT_IDENTITY_UNSTABLE =
  "The editor workspace kept changing while its files were listed.";

/** A listing naming another workspace re-resolves this many times before it is a fault. */
export const WORKSPACE_IDENTITY_RECONCILE_LIMIT = 3;

/** Transport gaps and an in-progress root transition can settle without user action. */
export function workspaceLookupRetryable(err: unknown): boolean {
  return err instanceof BackendTransportError ||
    (err instanceof LycaonApiError && err.code === "project_mutation_in_progress");
}

/** Hides transport details from workspace errors. */
export function workspaceFaultMessage(err: unknown): string {
  if (workspaceMismatchId(err)) return WORKSPACE_FAULT_IDENTITY_UNSTABLE;
  if (err instanceof LycaonApiError) {
    const message = err.message.trim();
    return message || WORKSPACE_FAULT_GENERIC;
  }
  return WORKSPACE_FAULT_GENERIC;
}
