import type {
  ArtifactListItem,
  VisualArtifactSource,
} from "../../api/types.ts";

export type ArtifactSourceFilter = "all" | VisualArtifactSource;
/** `"all"` | a concrete session_id (chat). */
export type ArtifactChatFilter = string;

export function filterProjectArtifacts(
  items: readonly ArtifactListItem[],
  source: ArtifactSourceFilter,
  chat: ArtifactChatFilter,
): ArtifactListItem[] {
  return items.filter((item) => {
    if (source !== "all" && item.source !== source) return false;
    if (chat === "all") return true;
    return item.session_id.trim() === chat;
  });
}

export function uniqueArtifactChatIds(
  items: readonly ArtifactListItem[],
): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const item of items) {
    const id = item.session_id.trim();
    if (!id || seen.has(id)) continue;
    seen.add(id);
    out.push(id);
  }
  return out;
}

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
