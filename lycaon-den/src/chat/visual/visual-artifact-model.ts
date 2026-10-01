import type { VisualArtifact } from "../../api/types.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";

/** Video artifacts use a media player. */
export function isVisualVideoMime(mime: string | null | undefined): boolean {
  return (mime ?? "").trim().toLowerCase().startsWith("video/");
}

/** Wire visual when the id is set. */
export function visualFromPart(part: ToolPartView): VisualArtifact | null {
  const visual = part.visual;
  if (!visual?.id.trim()) return null;
  return visual;
}

export function artifactFetchPath(
  sessionId: string,
  artifactId: string,
): string {
  return `/v1/sessions/${encodeURIComponent(sessionId)}/artifacts/${encodeURIComponent(artifactId)}`;
}
