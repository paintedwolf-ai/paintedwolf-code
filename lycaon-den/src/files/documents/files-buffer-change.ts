import type { FileBuffer } from "./files-buffer-state.ts";
import { isComposedBufferKind } from "./project-files-buffer-kind.ts";
import { scopeChangeKindFor } from "../tree/scope-resolution.ts";
import type { FileChange, ScopeChangeKind } from "../components/files-scope-change-mark.ts";
import type { FileVersionView } from "../history/file-version.ts";
import { sourceChangeFromPresence } from "../../components/source/reader/source-reader-change.ts";

/**
 * A shown version names the change it made. Otherwise a current source read
 * takes precedence over comparison deletion marks.
 */
export function filesBufferChange(
  projectId: string,
  buffer: FileBuffer,
  version: FileVersionView | null,
): FileChange | null {
  if (version) {
    const kind = fileVersionChangeKind(version);
    return kind ? { kind, subject: "version" } : null;
  }
  if (isComposedBufferKind(buffer.kind)) return null;
  if (buffer.deleted) return { kind: "deleted", subject: "view" };
  const kind = scopeChangeKindFor(projectId, buffer.rootId, buffer.path);
  if (!kind) return null;
  return { kind: kind === "deleted" && filesBufferHasCurrentFile(buffer) ? "changed" : kind, subject: "view" };
}

/** Presence on each side of the version's own comparison, independent of the Compare choice. */
export function fileVersionChangeKind(
  version: Pick<FileVersionView, "beforeAvailability" | "availability">,
): ScopeChangeKind | null {
  const change = sourceChangeFromPresence(version.beforeAvailability !== "absent", version.availability !== "absent");
  return change === "absent" ? null : change;
}

export function filesBufferHasCurrentFile(buffer: FileBuffer): boolean {
  return buffer.sourcePresent && !buffer.deleted && !buffer.loadError && !buffer.diverged;
}
