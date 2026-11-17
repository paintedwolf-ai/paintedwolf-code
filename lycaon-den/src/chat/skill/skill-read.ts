/** Titles for a skill read that names one of the skill's resources. */

import type { ToolPartView } from "../tool/tool-part-model.ts";

/** The resource named by a skill read, distinct from the skill overview. */
export function skillReadResource(part: Pick<ToolPartView, "tool" | "args">): string | null {
  if (part.tool !== "skills_read") return null;
  const resource = part.args?.resource;
  return typeof resource === "string" && resource.trim() ? resource.trim() : null;
}

export function skillResourceTitle(resource: string): string {
  const filename = resource.split("/").at(-1) ?? resource;
  return filename.replace(/\.[^.]+$/, "").replace(/[_-]+/g, " ");
}
