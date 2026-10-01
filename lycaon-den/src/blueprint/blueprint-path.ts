import { overlayRel } from "../platform/files/overlay-dir.ts";

const BLUEPRINT_ROOT = `${overlayRel("blueprints")}/`;

/** Bound blueprint path under the convention root, or empty when unbound. */
export function boundBlueprintRelPath(blueprintPath?: string | null): string {
  const bound = blueprintPath?.trim() ?? "";
  if (bound.startsWith(BLUEPRINT_ROOT) && bound.endsWith(".md")) {
    return bound;
  }
  return "";
}
