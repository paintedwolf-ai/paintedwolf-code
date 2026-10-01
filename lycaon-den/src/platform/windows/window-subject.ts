/** The narrow, immutable subject a pulled-off window was opened for. */
export type WindowSubject =
  | { kind: "session"; projectId: string; sessionId: string; viewId: string }
  | {
      kind: "file";
      projectId: string;
      rootId: string;
      path: string;
      viewId: string;
    }
  | { kind: "context"; projectId: string; stageId: string; viewId: string };

function queryValue(query: URLSearchParams, name: string): string | null {
  const value = query.get(name)?.trim();
  return value ? value : null;
}

export function windowSubject(): WindowSubject | null {
  if (typeof window === "undefined") return null;
  const query = new URLSearchParams(window.location.search);
  const kind = queryValue(query, "window_subject");
  const projectId = queryValue(query, "project_id");
  const viewId = queryValue(query, "view_id");
  if (!projectId || !viewId) return null;
  const stageId = queryValue(query, "stage_id");
  if (kind === "context" && stageId) return { kind, projectId, stageId, viewId };
  const sessionId = queryValue(query, "session_id");
  if (kind === "session" && sessionId) return { kind, projectId, sessionId, viewId };
  const rootId = queryValue(query, "root_id");
  const path = queryValue(query, "path");
  if (kind === "file" && rootId && path) {
    return {
      kind,
      projectId,
      rootId,
      path,
      viewId,
    };
  }
  return null;
}

/** Stable local identity for editing leases and view-scoped presentation. */
export function windowViewId(): string {
  return windowSubject()?.viewId ?? "main";
}
