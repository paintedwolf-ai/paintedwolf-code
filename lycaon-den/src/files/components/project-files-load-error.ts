import { LycaonApiError } from "../../api/http.ts";
import {
  applyFilesBufferLoadError,
  applyFilesBufferUnsupportedEncoding,
} from "../documents/project-files-buffers.ts";
import type { FileBufferKey } from "./project-files-model.ts";

/** Map a GET /source failure onto buffer state (info card vs load error). */
export function applyProjectSourceLoadFailure(
  projectId: string,
  key: FileBufferKey,
  err: unknown,
): void {
  if (
    err instanceof LycaonApiError &&
    err.code === "unsupported_encoding"
  ) {
    const raw = err.details?.detected;
    const detected =
      typeof raw === "string" && raw.trim() ? raw.trim() : "unknown";
    applyFilesBufferUnsupportedEncoding(projectId, key, detected);
    return;
  }
  applyFilesBufferLoadError(
    projectId,
    key,
    err instanceof Error ? err.message : "Failed to load file",
  );
}
