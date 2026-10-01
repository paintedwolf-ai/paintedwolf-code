/** Skill cards use the host projection and catalog rendering family. */

import type { SkillActivation } from "../../api/types.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";

/** Chicklet name slot for a skill row — the family, matching `read` / `command`. */
export const SKILL_CHICKLET_NAME = "skill";

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

/** Rows the catalog routes to SkillCard. */
export function isSkillToolPart(part: ToolPartView): boolean {
  return part.kind === "skill";
}

/** Discovery, pending, and rejected reads have no skill projection. */
export function skillActivationFromPart(
  part: ToolPartView,
): SkillActivation | null {
  const skill = part.skill;
  if (!skill) return null;
  return skill.name.trim() ? skill : null;
}

/** Files the activation listed, plus the count it had to omit. */
export type SkillResourceListing = {
  files: readonly string[];
  omitted: number;
};

export function skillResourceListing(
  skill: SkillActivation,
): SkillResourceListing | null {
  const files = (skill.resources ?? []).filter((f) => f.trim());
  const omitted = skill.resources_omitted ?? 0;
  if (!files.length && omitted <= 0) return null;
  return { files, omitted };
}

/** Where the skill came from, in the vocabulary the settings surfaces use. */
export function skillOriginLabel(skill: SkillActivation): string {
  if (skill.project) return "Project";
  const pack = skill.pack_id?.trim();
  return pack ? `Pack · ${pack}` : "Host";
}
