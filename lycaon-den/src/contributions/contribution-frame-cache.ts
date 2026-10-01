import { CONFIG_DIR_NAME } from "../../shared/brand.ts";
import type { ContributionFrameResponse } from "../api/types.ts";
import { isRecord } from "../utils/type-guards.ts";

export const CONTRIBUTION_FRAME_STORAGE_KEY = `${CONFIG_DIR_NAME}.contribution-frame`;
const CACHE_VERSION = 1;
const COLLECTIONS = ["commands", "menus", "keybindings", "binding_defaults", "editor_actions", "configuration",
  "requirements", "search_sources", "operations", "themes", "notes"] as const satisfies readonly (keyof ContributionFrameResponse)[];

const finiteBetween = (value: unknown, min: number, max: number): value is number =>
  typeof value === "number" && Number.isFinite(value) && value >= min && value <= max;
const opaqueColor = (value: unknown): boolean => typeof value === "string" && /^#[\da-f]{6}$/i.test(value);

function hasWindowColors(value: unknown): boolean {
  return isRecord(value) && opaqueColor(value.main) && Array.isArray(value.anchors)
    && value.anchors.length >= 1 && value.anchors.length <= 12 && value.anchors.every(opaqueColor)
    && finiteBetween(value.hue_spread, 0, 180) && finiteBetween(value.chroma_min, 0, 0.4)
    && finiteBetween(value.chroma_max, value.chroma_min, 0.4);
}

function hasAgentColors(value: unknown): boolean {
  return isRecord(value) && opaqueColor(value.main) && finiteBetween(value.lightness_step, 0.02, 0.25)
    && finiteBetween(value.chroma, 0, 0.12);
}

function hasThemePaint(value: unknown): boolean {
  return isRecord(value) && typeof value.id === "string" && typeof value.name === "string"
    && (value.appearance === "light" || value.appearance === "dark")
    && (value.logomark === "shown" || value.logomark === "hidden")
    && isRecord(value.tokens) && Object.values(value.tokens).every(token => typeof token === "string")
    && isRecord(value.syntax) && Object.values(value.syntax).every(style => isRecord(style) && typeof style.color === "string")
    && isRecord(value.icon_stroke) && finiteBetween(value.icon_stroke.weight, 0.5, 2)
    && (value.icon_stroke.cap === "butt" || value.icon_stroke.cap === "round" || value.icon_stroke.cap === "square")
    && (value.icon_stroke.join === "miter" || value.icon_stroke.join === "round" || value.icon_stroke.join === "bevel")
    && (value.icons === undefined || (isRecord(value.icons) && Object.values(value.icons).every(Array.isArray)))
    && hasWindowColors(value.window_colors) && hasAgentColors(value.agent_colors);
}

/** Checks paint data before startup consumers read it. */
export function hasContributionFrameShape(value: unknown): value is ContributionFrameResponse {
  return isRecord(value) && typeof value.frame_revision === "string" && COLLECTIONS.every(key => Array.isArray(value[key]))
    && Array.isArray(value.themes) && value.themes.every(hasThemePaint);
}

export function readContributionFrameCache(): ContributionFrameResponse | null {
  try {
    if (typeof localStorage === "undefined") return null;
    const raw = localStorage.getItem(CONTRIBUTION_FRAME_STORAGE_KEY);
    if (!raw) return null;
    const memo: unknown = JSON.parse(raw);
    if (isRecord(memo) && memo.version === CACHE_VERSION && hasContributionFrameShape(memo.frame)) return memo.frame;
  } catch { /* Invalid cache entries are discarded. */ }
  clearContributionFrameCache();
  return null;
}

export function writeContributionFrameCache(frame: ContributionFrameResponse): void {
  try {
    if (typeof localStorage !== "undefined") localStorage.setItem(CONTRIBUTION_FRAME_STORAGE_KEY,
      JSON.stringify({ version: CACHE_VERSION, frame }));
  } catch { /* The live frame remains available if caching fails. */ }
}

export function clearContributionFrameCache(): void {
  try {
    if (typeof localStorage !== "undefined") localStorage.removeItem(CONTRIBUTION_FRAME_STORAGE_KEY);
  } catch { /* Storage may be unavailable. */ }
}
