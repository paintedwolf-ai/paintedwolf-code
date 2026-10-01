import { LycaonApiError } from "../../api/http.ts";

export class SourceWorkspaceMismatchError extends Error {
  constructor(
    readonly expectedWorkspaceId: string,
    readonly actualWorkspaceId: string,
  ) {
    super("The source workspace changed while files were loading.");
    this.name = "SourceWorkspaceMismatchError";
  }
}

export function workspaceMismatchId(error: unknown): string {
  if (error instanceof SourceWorkspaceMismatchError) {
    return typeof error.actualWorkspaceId === "string"
      ? error.actualWorkspaceId.trim()
      : "";
  }
  if (
    error instanceof LycaonApiError &&
    error.code === "source_workspace_mismatch" &&
    typeof error.details?.actual_workspace_id === "string"
  ) {
    return error.details.actual_workspace_id.trim();
  }
  return "";
}
