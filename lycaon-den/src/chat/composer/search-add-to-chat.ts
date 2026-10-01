import type { SearchHit } from "../../api/types.ts";
import {
  basenameOfPath,
  relativeUnderRoot,
  longestMatchingRoot,
  resolveProjectFile,
  type ResolveProjectRoot,
} from "../../api/project-path.ts";
import { searchHitDisplay } from "../../search/search-hit-display.ts";
import type { ChatAttachmentRef } from "./add-to-chat.ts";


// Returns null without host-resolvable coordinates.
export function chatRefForSearchHit(
  hit: SearchHit,
  rootRefs: readonly ResolveProjectRoot[],
): ChatAttachmentRef | null {
  const projectId = hit.project_id?.trim() ?? "";
  if (!projectId) return null;

  const display = searchHitDisplay(hit);
  const openPath = display.openPath?.path?.trim();
  if (openPath && rootRefs.length > 0) {
    const resolved = resolveProjectFile({ roots: rootRefs }, openPath);
    if (!("error" in resolved)) {
      const containingRoot = longestMatchingRoot(resolved.absolutePath, rootRefs);
      const rel = containingRoot
        ? relativeUnderRoot(resolved.absolutePath, containingRoot.path)
        : null;
      if (containingRoot && rel != null) {
        return {
          kind: "path-file",
          projectId,
          rootId: containingRoot.id,
          path: rel,
          name: basenameOfPath(rel === "." ? containingRoot.path : rel),
        };
      }
    }
  }

  const sourceRef = hit.source_ref?.trim() ?? "";
  if (hit.hit_kind === "artifact" && sourceRef) {
    return {
      kind: "artifact",
      projectId,
      artifactId: sourceRef,
      name: display.title,
    };
  }

  const sessionId = hit.session_id?.trim() ?? "";
  const hitKind = (hit.hit_kind ?? "").trim();
  if (!sourceRef || !sessionId || !hitKind) return null;

  return {
    kind: "search-hit",
    projectId,
    sessionId,
    sourceRef,
    hitKind,
    name: display.title,
  };
}

export type ToolRawAddCoords = {
  projectId: string;
  sessionId: string;
  toolCallId: string;
  name?: string;
};

// Tool raw refs use the same coordinates as search navigation.
export function chatRefForToolRaw(
  coords: ToolRawAddCoords,
): ChatAttachmentRef | null {
  const projectId = coords.projectId.trim();
  const sessionId = coords.sessionId.trim();
  const toolCallId = coords.toolCallId.trim();
  if (!projectId || !sessionId || !toolCallId) return null;
  const name = coords.name?.trim() || toolCallId;
  return {
    kind: "search-hit",
    projectId,
    sessionId,
    sourceRef: toolCallId,
    hitKind: "tool",
    name,
  };
}
