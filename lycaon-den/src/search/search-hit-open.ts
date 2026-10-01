import type { SearchHit } from "../api/types.ts";
import { searchHitDisplay } from "./search-hit-display.ts";
import { openSourceLocation } from "../platform/navigation/open-source.ts";
import { confirmAndOpenExternalLink } from "../platform/desktop/external-link.ts";

// File and URL targets bypass transcript navigation.
export function openSearchHit(
  hit: SearchHit,
  navigate: (hit: SearchHit) => void,
): void {
  const display = searchHitDisplay(hit);
  const projectId = hit.project_id?.trim() ?? "";
  if (display.openPath && projectId) {
    void openSourceLocation({
      intent: "transient",
      projectId,
      rootId: hit.root_id,
      path: display.openPath.path,
      entryKind: "file",
      line: display.openPath.line,
      focus: true,
    });
    return;
  }
  if (display.openUrl) {
    void confirmAndOpenExternalLink(display.openUrl);
    return;
  }
  navigate(hit);
}
