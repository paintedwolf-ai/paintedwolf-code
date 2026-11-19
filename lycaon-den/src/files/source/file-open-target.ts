import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { outsideHostPath } from "./outside-namespace.ts";
import type { LocalPathTarget } from "../../platform/navigation/open-local-path.ts";
import type { ProjectRoot } from "../../api/types.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { absolutePathForBuffer } from "../components/project-files-model.ts";

export function fileOpenTarget(
  buffer: Pick<FileBuffer, "kind" | "rootId" | "path" | "jobId" | "sourcePresent">,
  roots: readonly Pick<ProjectRoot, "id" | "path">[],
  line?: number,
  outsideRootId?: string,
): LocalPathTarget | null {
  if (isComposedBufferKind(buffer.kind)) return null;
  if (outsideRootId && buffer.rootId === outsideRootId) {
    return { absolutePath: outsideHostPath(buffer.path), projectRoots: roots.map((candidate) => candidate.path), entryKind: "file", line,
      unavailable: buffer.sourcePresent ? undefined : "This file is not present on disk." };
  }
  const root = roots.find((candidate) => candidate.id === buffer.rootId);
  return {
    absolutePath: root ? absolutePathForBuffer(root.path, buffer.path) : buffer.path,
    projectRoots: roots.map((candidate) => candidate.path), entryKind: "file", line,
    unavailable: buffer.jobId ? "This action requires the worker workspace location."
      : !root ? "The containing folder is unavailable."
      : !buffer.sourcePresent ? "This file is not present on disk."
      : undefined,
  };
}
