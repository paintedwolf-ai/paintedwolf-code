import { basenameOfPath } from "../api/project-path.ts";

/** Opening labels follow the host's project naming rules. */

/** A folder opened as a project is named after its final path segment. */
export function folderWorkspaceLabel(path: string): string {
  return basenameOfPath(path.trim());
}

/** A cloned repository is named after the clone URL's last segment without `.git`. */
export function repoWorkspaceLabel(url: string): string {
  const trimmed = url.trim().replace(/\/+$/, "");
  const cut = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf(":"));
  const tail = cut >= 0 ? trimmed.slice(cut + 1) : trimmed;
  return tail.replace(/\.git$/, "");
}
