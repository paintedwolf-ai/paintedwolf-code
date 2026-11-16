import type { VisualArtifactSource } from "../../api/types.ts";

export type ArtifactSourceFilter = "all" | VisualArtifactSource;
/** `"all"` or a concrete session_id. */
export type ArtifactChatFilter = string;

export function artifactSourceLabel(source: VisualArtifactSource): string {
  switch (source) {
    case "render":
      return "Render";
    case "capture":
      return "Capture";
    case "fetch":
      return "Fetch";
    case "user":
      return "User";
    case "workspace":
      return "Workspace";
  }
}

export function shortChatLabel(sessionId: string): string {
  const id = sessionId.trim();
  if (id.length <= 8) return id || "chat";
  return id.slice(0, 8);
}
