import type { BlueprintSummary, WorkflowSummary } from "../../api/types.ts";

/** Cap mirror of host MAX_PROJECT_BLUEPRINTS — list is already clamped server-side. */
export const PROJECT_BLUEPRINTS_LIST_CAP = 100;

/** Display-title max length (mirrors host MaxBlueprintTitleLen). */
export const BLUEPRINT_TITLE_MAX_LEN = 120;

export type BlueprintListSortKey = "title" | "updated";
export type BlueprintListSortDir = "asc" | "desc";

export function blueprintListTitle(bp: BlueprintSummary): string {
  const title = bp.title?.trim() ?? "";
  return title || "Untitled blueprint";
}

/** Secondary path label under the title (basename when nested). */
export function blueprintPathLabel(bp: BlueprintSummary): string {
  const path = bp.path?.trim() ?? "";
  if (!path) return "";
  const slash = path.lastIndexOf("/");
  return slash >= 0 ? path.slice(slash + 1) : path;
}

/** Derives a kind label from workflows or the filename. */
export function blueprintKindLabel(bp: BlueprintSummary): string {
  const compat = (bp.compatible_workflows ?? [])
    .map((id) => id.trim())
    .filter(Boolean);
  if (compat.includes("plan") && !compat.includes("options")) return "Plan";
  if (compat.includes("options") && !compat.includes("plan")) return "Decide";
  const path = bp.path?.trim() ?? "";
  if (!path) return "Blueprint";
  const slash = path.lastIndexOf("/");
  const base = slash >= 0 ? path.slice(slash + 1) : path;
  if (base === "options-selection.md") return "Decide";
  return base || "Blueprint";
}

export function defaultCompatibleWorkflow(
  compatible: readonly string[] | undefined,
): string | undefined {
  const ids = (compatible ?? []).map((id) => id.trim()).filter(Boolean);
  if (ids.length === 0) return undefined;
  if (ids.includes("plan")) return "plan";
  return ids[0];
}

export function workflowPickerLabel(
  workflowId: string,
  catalog: readonly WorkflowSummary[] | undefined,
): string {
  const id = workflowId.trim();
  if (!id) return "Workflow";
  const hit = catalog?.find((w) => w.id === id);
  const name = hit?.name?.trim();
  if (name) return name;
  if (id === "plan") return "Plan";
  if (id === "options") return "Decide";
  return id;
}

export function clampBlueprintList(
  rows: readonly BlueprintSummary[],
): BlueprintSummary[] {
  return rows.slice(0, PROJECT_BLUEPRINTS_LIST_CAP);
}

export function defaultBlueprintListSortDir(
  key: BlueprintListSortKey,
): BlueprintListSortDir {
  return key === "updated" ? "desc" : "asc";
}

export function nextBlueprintListSortDir(
  currentKey: BlueprintListSortKey,
  nextKey: BlueprintListSortKey,
  currentDir: BlueprintListSortDir,
): BlueprintListSortDir {
  if (currentKey === nextKey) {
    return currentDir === "asc" ? "desc" : "asc";
  }
  return defaultBlueprintListSortDir(nextKey);
}

export function sortBlueprintSummaries(
  rows: readonly BlueprintSummary[],
  key: BlueprintListSortKey,
  dir: BlueprintListSortDir,
): BlueprintSummary[] {
  const mul = dir === "asc" ? 1 : -1;
  return [...rows].sort((a, b) => {
    if (key === "title") {
      const byTitle = blueprintListTitle(a).localeCompare(
        blueprintListTitle(b),
        undefined,
        { sensitivity: "base" },
      );
      if (byTitle !== 0) return mul * byTitle;
      return (a.path ?? "").localeCompare(b.path ?? "");
    }
    const aMs = Date.parse(a.updated_at ?? "");
    const bMs = Date.parse(b.updated_at ?? "");
    const aSafe = Number.isFinite(aMs) ? aMs : 0;
    const bSafe = Number.isFinite(bMs) ? bMs : 0;
    if (aSafe !== bSafe) return mul * (aSafe - bSafe);
    const byTitle = blueprintListTitle(a).localeCompare(
      blueprintListTitle(b),
      undefined,
      { sensitivity: "base" },
    );
    if (byTitle !== 0) return byTitle;
    return (a.path ?? "").localeCompare(b.path ?? "");
  });
}
